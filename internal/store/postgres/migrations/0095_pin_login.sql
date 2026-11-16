-- 0095_pin_login.sql — first-party sign-in by e-mail and a 6-digit PIN, with an optional authenticator (TOTP).
--
-- WHY HERE AND HOW IT IS CONTAINED. Sign-in happens before the company (tenant) is known, so the credentials
-- cannot sit behind the per-tenant row-level security. They live in login_credentials, which NO runtime role may
-- read or write directly: every access goes through the SECURITY DEFINER functions below.
--   * Before authentication the API uses its pre-authentication login (role serk_abuse, the same login that
--     keeps the shared abuse counters, 0029). It may only look an e-mail up (login_lookup) and record the
--     outcome of an attempt (login_record).
--   * After authentication a tenant session (serk_app) manages the credentials of ITS OWN people only: every
--     tenant function checks the row's tenant against current_tenant(), which derives from the database login.
--
-- WHAT THE DATABASE NEVER HOLDS. The PIN is hashed by the API with a server-side secret (pepper) before PBKDF2,
-- and the authenticator secret is encrypted by the API; neither the pepper nor a key is stored here. A copy of
-- the database therefore does not allow the 1,000,000 possible PINs to be tried offline.
--
-- LOCKOUT. 15 consecutive wrong attempts lock the account for 15 minutes; the database counts them, on its own
-- clock, under a row lock, so concurrent attempts on several API replicas cannot exceed the limit. A success
-- resets the count. An authenticator code is accepted once only (the step is recorded; a replay is refused).
--
-- TRACE. Every attempt (success, wrong PIN, wrong code, locked, unknown e-mail, PIN change) and every
-- administrative action is written to login_events, which is append-only. Client addresses are stored only as
-- an HMAC.

CREATE TABLE login_credentials (
  email           text PRIMARY KEY CHECK (email = lower(btrim(email)) AND email ~ '^[^@\s]+@[^@\s]+\.[^@\s]+$' AND length(email) <= 254),
  tenant_id       uuid NOT NULL REFERENCES tenants(id),
  user_id         text NOT NULL CHECK (user_id ~ '^[A-Za-z0-9._@:+|-]{1,200}$'),
  pin_salt        bytea NOT NULL CHECK (octet_length(pin_salt) = 16),
  pin_hash        bytea NOT NULL CHECK (octet_length(pin_hash) = 32),
  pin_params      text NOT NULL CHECK (pin_params ~ '^pbkdf2-sha256-hmac:[0-9]{5,7}$'),
  must_change     boolean NOT NULL DEFAULT true,
  totp_secret_enc bytea,
  totp_enabled    boolean NOT NULL DEFAULT false,
  totp_last_step  bigint NOT NULL DEFAULT 0,
  failed_count    integer NOT NULL DEFAULT 0 CHECK (failed_count BETWEEN 0 AND 15),
  locked_until    timestamptz,
  disabled        boolean NOT NULL DEFAULT false,
  last_success_at timestamptz,
  last_failure_at timestamptz,
  pin_set_by      text NOT NULL,
  pin_set_at      timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, user_id),
  CONSTRAINT login_totp_needs_secret CHECK (NOT totp_enabled OR totp_secret_enc IS NOT NULL)
);
REVOKE ALL ON login_credentials FROM PUBLIC;

CREATE TABLE login_events (
  id         bigserial PRIMARY KEY,
  at         timestamptz NOT NULL DEFAULT clock_timestamp(),
  tenant_id  uuid REFERENCES tenants(id),
  user_id    text,
  email      text NOT NULL CHECK (length(email) <= 254),
  outcome    text NOT NULL CHECK (outcome IN ('SUCCESS', 'WRONG_PIN', 'WRONG_CODE', 'CODE_REPLAYED', 'LOCKED', 'NOW_LOCKED',
                                              'UNKNOWN_EMAIL', 'DISABLED', 'PIN_CHANGED', 'PIN_ISSUED', 'UNLOCKED',
                                              'AUTHENTICATOR_ON', 'AUTHENTICATOR_OFF', 'AUTHENTICATOR_RESET', 'LOGIN_DISABLED', 'LOGIN_ENABLED')),
  ip_hash    bytea CHECK (ip_hash IS NULL OR octet_length(ip_hash) = 32),
  actor      text,
  detail     text CHECK (detail IS NULL OR length(detail) <= 500)
);
CREATE INDEX login_events_tenant ON login_events (tenant_id, at DESC);
REVOKE ALL ON login_events FROM PUBLIC;
REVOKE ALL ON SEQUENCE login_events_id_seq FROM PUBLIC;

-- Append-only, also against the owner's own functions.
CREATE FUNCTION login_events_append_only() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  RAISE EXCEPTION 'login events are never changed or deleted' USING ERRCODE = 'check_violation';
END$$;
CREATE TRIGGER login_events_append_only BEFORE UPDATE OR DELETE ON login_events FOR EACH ROW EXECUTE FUNCTION login_events_append_only();

-- ------------------------------------------------------------------ before authentication (serk_abuse)

-- The stored credential for an e-mail, or no row. The API verifies the PIN with its pepper.
CREATE FUNCTION login_lookup(p_email text)
RETURNS TABLE (tenant_id uuid, user_id text, pin_salt bytea, pin_hash bytea, pin_params text, must_change boolean,
               totp_enabled boolean, totp_secret_enc bytea, totp_last_step bigint, locked_until timestamptz, disabled boolean)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp AS $$
  SELECT c.tenant_id, c.user_id, c.pin_salt, c.pin_hash, c.pin_params, c.must_change, c.totp_enabled, c.totp_secret_enc,
         c.totp_last_step, CASE WHEN c.locked_until > clock_timestamp() THEN c.locked_until END, c.disabled
    FROM login_credentials c WHERE c.email = lower(btrim(p_email))
$$;

-- Records one attempt and returns what the API must answer. p_outcome is what the API found:
-- SUCCESS, WRONG_PIN or WRONG_CODE (UNKNOWN_EMAIL when login_lookup found nothing; DISABLED when the right PIN
-- belongs to a person turned off in the company's directory). For a SUCCESS that used an
-- authenticator code, p_totp_step is its time step: one at or below the last accepted step is a replay.
-- p_new_salt / p_new_hash replace the PIN in the same step (a required PIN change at sign-in).
-- Returns: the final outcome, the lock end (when locked) and the consecutive failures so far.
CREATE FUNCTION login_record(p_email text, p_outcome text, p_ip_hash bytea, p_totp_step bigint, p_new_salt bytea, p_new_hash bytea, p_new_params text)
RETURNS TABLE (outcome text, locked_until timestamptz, failures integer)
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE c login_credentials; t timestamptz := clock_timestamp(); res text := p_outcome;
BEGIN
  IF p_outcome NOT IN ('SUCCESS', 'WRONG_PIN', 'WRONG_CODE', 'UNKNOWN_EMAIL', 'DISABLED') THEN
    RAISE EXCEPTION 'login_record: invalid outcome' USING ERRCODE = 'check_violation';
  END IF;
  SELECT * INTO c FROM login_credentials WHERE email = lower(btrim(p_email)) FOR UPDATE;
  IF c.email IS NULL THEN
    INSERT INTO login_events (email, outcome, ip_hash) VALUES (left(lower(btrim(p_email)), 254), 'UNKNOWN_EMAIL', p_ip_hash);
    RETURN QUERY SELECT 'UNKNOWN_EMAIL'::text, NULL::timestamptz, 0;
    RETURN;
  END IF;
  IF c.disabled OR p_outcome = 'DISABLED' THEN
    INSERT INTO login_events (tenant_id, user_id, email, outcome, ip_hash, detail)
    VALUES (c.tenant_id, c.user_id, c.email, 'DISABLED', p_ip_hash, CASE WHEN c.disabled THEN 'sign-in turned off' ELSE 'person turned off in the directory' END);
    RETURN QUERY SELECT 'DISABLED'::text, NULL::timestamptz, c.failed_count;
    RETURN;
  END IF;
  -- A locked account answers LOCKED whatever was typed: a correct PIN during the lock does not open it.
  IF c.locked_until > t THEN
    INSERT INTO login_events (tenant_id, user_id, email, outcome, ip_hash) VALUES (c.tenant_id, c.user_id, c.email, 'LOCKED', p_ip_hash);
    RETURN QUERY SELECT 'LOCKED'::text, c.locked_until, c.failed_count;
    RETURN;
  END IF;
  IF p_outcome = 'SUCCESS' AND p_totp_step IS NOT NULL AND p_totp_step <= c.totp_last_step THEN
    res := 'CODE_REPLAYED';
  END IF;
  IF p_outcome = 'SUCCESS' AND c.must_change AND p_new_hash IS NULL THEN
    RAISE EXCEPTION 'login_record: a PIN that must be changed is replaced in the same step' USING ERRCODE = 'check_violation';
  END IF;
  IF res = 'SUCCESS' THEN
    UPDATE login_credentials SET failed_count = 0, locked_until = NULL, last_success_at = t,
           totp_last_step = GREATEST(totp_last_step, COALESCE(p_totp_step, 0)),
           pin_salt = COALESCE(p_new_salt, pin_salt), pin_hash = COALESCE(p_new_hash, pin_hash),
           pin_params = COALESCE(p_new_params, pin_params),
           must_change = CASE WHEN p_new_hash IS NOT NULL THEN false ELSE must_change END,
           pin_set_by = CASE WHEN p_new_hash IS NOT NULL THEN c.user_id ELSE pin_set_by END,
           pin_set_at = CASE WHEN p_new_hash IS NOT NULL THEN t ELSE pin_set_at END
     WHERE email = c.email;
    IF p_new_hash IS NOT NULL THEN
      INSERT INTO login_events (tenant_id, user_id, email, outcome, ip_hash, actor, detail)
      VALUES (c.tenant_id, c.user_id, c.email, 'PIN_CHANGED', p_ip_hash, c.user_id, 'changed at sign-in');
    END IF;
    INSERT INTO login_events (tenant_id, user_id, email, outcome, ip_hash, detail)
    VALUES (c.tenant_id, c.user_id, c.email, 'SUCCESS', p_ip_hash, CASE WHEN p_totp_step IS NOT NULL THEN 'with authenticator' END);
    RETURN QUERY SELECT 'SUCCESS'::text, NULL::timestamptz, 0;
    RETURN;
  END IF;
  -- A failure: 15 in a row lock the account for 15 minutes; the count starts again after the lock.
  IF c.failed_count + 1 >= 15 THEN
    UPDATE login_credentials SET failed_count = 0, locked_until = t + interval '15 minutes', last_failure_at = t WHERE email = c.email;
    INSERT INTO login_events (tenant_id, user_id, email, outcome, ip_hash, detail)
    VALUES (c.tenant_id, c.user_id, c.email, 'NOW_LOCKED', p_ip_hash, res || ': 15th consecutive failure');
    RETURN QUERY SELECT 'NOW_LOCKED'::text, t + interval '15 minutes', 15;
    RETURN;
  END IF;
  UPDATE login_credentials SET failed_count = c.failed_count + 1, last_failure_at = t WHERE email = c.email;
  INSERT INTO login_events (tenant_id, user_id, email, outcome, ip_hash) VALUES (c.tenant_id, c.user_id, c.email, res, p_ip_hash);
  RETURN QUERY SELECT res, NULL::timestamptz, c.failed_count + 1;
END$$;

-- ------------------------------------------------------------------ after authentication (serk_app, own tenant only)

CREATE FUNCTION login_require_tenant() RETURNS uuid
LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE t uuid := current_tenant();
BEGIN
  IF t IS NULL THEN
    RAISE EXCEPTION 'login administration needs a tenant session' USING ERRCODE = 'insufficient_privilege';
  END IF;
  RETURN t;
END$$;

-- Issues (or re-issues) a person's sign-in: e-mail and a PIN hash computed by the API. Re-issuing replaces the PIN,
-- clears the lock and requires a change at the next sign-in when p_must_change. The person must be in this
-- tenant's directory and active.
CREATE FUNCTION login_issue(p_user text, p_email text, p_salt bytea, p_hash bytea, p_params text, p_must_change boolean, p_by text)
RETURNS void LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE t uuid := login_require_tenant(); e text := lower(btrim(p_email)); other login_credentials;
BEGIN
  IF NOT EXISTS (SELECT 1 FROM directory_users d WHERE d.tenant_id = t AND d.user_id = p_user AND d.status = 'ACTIVE') THEN
    RAISE EXCEPTION 'no active person % in this company', p_user USING ERRCODE = 'check_violation';
  END IF;
  SELECT * INTO other FROM login_credentials WHERE email = e;
  IF other.email IS NOT NULL AND (other.tenant_id <> t OR other.user_id <> p_user) THEN
    RAISE EXCEPTION 'this e-mail address is already used for another sign-in' USING ERRCODE = 'unique_violation';
  END IF;
  DELETE FROM login_credentials WHERE tenant_id = t AND user_id = p_user AND email <> e;
  INSERT INTO login_credentials (email, tenant_id, user_id, pin_salt, pin_hash, pin_params, must_change, pin_set_by)
  VALUES (e, t, p_user, p_salt, p_hash, p_params, p_must_change, p_by)
  ON CONFLICT (email) DO UPDATE SET pin_salt = EXCLUDED.pin_salt, pin_hash = EXCLUDED.pin_hash, pin_params = EXCLUDED.pin_params,
    must_change = EXCLUDED.must_change, pin_set_by = EXCLUDED.pin_set_by, pin_set_at = now(), failed_count = 0, locked_until = NULL;
  INSERT INTO login_events (tenant_id, user_id, email, outcome, actor, detail)
  VALUES (t, p_user, e, CASE WHEN p_by = p_user THEN 'PIN_CHANGED' ELSE 'PIN_ISSUED' END, p_by,
          CASE WHEN p_must_change THEN 'must be changed at the next sign-in' END);
END$$;

-- The stored PIN of one of this tenant's people (to check a current PIN before a change).
CREATE FUNCTION login_own_credential(p_user text)
RETURNS TABLE (email text, pin_salt bytea, pin_hash bytea, pin_params text, totp_enabled boolean, totp_secret_enc bytea, totp_last_step bigint)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp AS $$
  SELECT c.email, c.pin_salt, c.pin_hash, c.pin_params, c.totp_enabled, c.totp_secret_enc, c.totp_last_step
    FROM login_credentials c WHERE c.tenant_id = login_require_tenant() AND c.user_id = p_user
$$;

-- Administrative and self-service changes. p_action: UNLOCK, AUTHENTICATOR_RESET, LOGIN_DISABLED, LOGIN_ENABLED
-- (administrators); AUTHENTICATOR_ON (with the encrypted secret and the confirming code's step), AUTHENTICATOR_OFF
-- (the person). The API decides who may do which; the database keeps it inside the tenant and records it.
CREATE FUNCTION login_change(p_user text, p_action text, p_secret_enc bytea, p_step bigint, p_by text, p_reason text)
RETURNS void LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE t uuid := login_require_tenant(); c login_credentials;
BEGIN
  SELECT * INTO c FROM login_credentials WHERE tenant_id = t AND user_id = p_user FOR UPDATE;
  IF c.email IS NULL THEN
    RAISE EXCEPTION 'person % has no sign-in in this company', p_user USING ERRCODE = 'no_data_found';
  END IF;
  CASE p_action
  WHEN 'UNLOCK' THEN UPDATE login_credentials SET locked_until = NULL, failed_count = 0 WHERE email = c.email;
  WHEN 'AUTHENTICATOR_RESET', 'AUTHENTICATOR_OFF' THEN
    UPDATE login_credentials SET totp_enabled = false, totp_secret_enc = NULL WHERE email = c.email;
  WHEN 'AUTHENTICATOR_ON' THEN
    IF p_secret_enc IS NULL OR p_step IS NULL OR p_step <= c.totp_last_step THEN
      RAISE EXCEPTION 'an authenticator is turned on with its secret and a fresh confirming code' USING ERRCODE = 'check_violation';
    END IF;
    UPDATE login_credentials SET totp_enabled = true, totp_secret_enc = p_secret_enc, totp_last_step = p_step WHERE email = c.email;
  WHEN 'LOGIN_DISABLED' THEN UPDATE login_credentials SET disabled = true WHERE email = c.email;
  WHEN 'LOGIN_ENABLED' THEN UPDATE login_credentials SET disabled = false, failed_count = 0, locked_until = NULL WHERE email = c.email;
  ELSE
    RAISE EXCEPTION 'login_change: unknown action %', p_action USING ERRCODE = 'check_violation';
  END CASE;
  INSERT INTO login_events (tenant_id, user_id, email, outcome, actor, detail)
  VALUES (t, p_user, c.email, CASE p_action WHEN 'UNLOCK' THEN 'UNLOCKED' ELSE p_action END, p_by, NULLIF(left(btrim(COALESCE(p_reason, '')), 500), ''));
END$$;

-- The sign-in state of this tenant's people (never a hash or a secret).
CREATE FUNCTION login_accounts()
RETURNS TABLE (user_id text, email text, must_change boolean, totp_enabled boolean, failed_count integer, locked_until timestamptz,
               disabled boolean, last_success_at timestamptz, last_failure_at timestamptz, pin_set_by text, pin_set_at timestamptz)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp AS $$
  SELECT c.user_id, c.email, c.must_change, c.totp_enabled, c.failed_count,
         CASE WHEN c.locked_until > clock_timestamp() THEN c.locked_until END, c.disabled, c.last_success_at, c.last_failure_at,
         c.pin_set_by, c.pin_set_at
    FROM login_credentials c WHERE c.tenant_id = login_require_tenant() ORDER BY c.email
$$;

-- This tenant's sign-in trail, newest first (optionally one person's).
CREATE FUNCTION login_trail(p_user text, p_limit integer)
RETURNS TABLE (at timestamptz, user_id text, email text, outcome text, actor text, detail text)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp AS $$
  SELECT e.at, e.user_id, e.email, e.outcome, e.actor, e.detail FROM login_events e
   WHERE e.tenant_id = login_require_tenant() AND (p_user = '' OR e.user_id = p_user)
   ORDER BY e.id DESC LIMIT LEAST(GREATEST(p_limit, 1), 1000)
$$;

REVOKE ALL ON FUNCTION login_lookup(text), login_record(text, text, bytea, bigint, bytea, bytea, text), login_require_tenant(),
  login_issue(text, text, bytea, bytea, text, boolean, text), login_own_credential(text), login_change(text, text, bytea, bigint, text, text),
  login_accounts(), login_trail(text, integer), login_events_append_only() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION login_lookup(text), login_record(text, text, bytea, bigint, bytea, bytea, text) TO serk_abuse;
GRANT EXECUTE ON FUNCTION login_issue(text, text, bytea, bytea, text, boolean, text), login_own_credential(text),
  login_change(text, text, bytea, bigint, text, text), login_accounts(), login_trail(text, integer) TO serk_app;
