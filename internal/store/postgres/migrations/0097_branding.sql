-- 0097_branding.sql — the company's own look: name, motto, address, contact details, colours and logo.
--
-- tenant_branding holds the words shown on the sign-in page and in the console (brand name and suffix, motto,
-- eyebrow line, description, address, phone, e-mail, website) and the theme: colour tokens for light and dark
-- mode, corner radius and font, as a JSON object the API validates (known keys, #rrggbb colours, readable
-- contrast). tenant_logos holds the logo: PNG, JPEG or WebP, at most 10 MB (10,485,760 bytes), with its SHA-256
-- and pixel size. SVG is refused because it can carry script. Both are changed by tenant.settings.manage
-- holders, versioned and recorded in the audit log; anyone signed in to the company may read them, and the
-- sign-in page shows the deployment's configured company before anyone signs in.

CREATE TABLE tenant_branding (
  tenant_id     uuid PRIMARY KEY DEFAULT current_tenant() REFERENCES tenants(id),
  brand_name    text NOT NULL DEFAULT '' CHECK (length(brand_name) <= 80),
  brand_suffix  text NOT NULL DEFAULT '' CHECK (length(brand_suffix) <= 40),
  eyebrow       text NOT NULL DEFAULT '' CHECK (length(eyebrow) <= 80),
  motto         text NOT NULL DEFAULT '' CHECK (length(motto) <= 120),
  description   text NOT NULL DEFAULT '' CHECK (length(description) <= 300),
  address       text NOT NULL DEFAULT '' CHECK (length(address) <= 500),
  phone         text NOT NULL DEFAULT '' CHECK (length(phone) <= 40),
  email         text NOT NULL DEFAULT '' CHECK (length(email) <= 254 AND (email = '' OR email ~ '^[^@\s]+@[^@\s]+\.[^@\s]+$')),
  website       text NOT NULL DEFAULT '' CHECK (length(website) <= 200 AND (website = '' OR website ~ '^https://[^\s<>"]+$')),
  theme         jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(theme) = 'object' AND octet_length(theme::text) <= 8192),
  version       integer NOT NULL DEFAULT 1,
  updated_by    text NOT NULL,
  updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE tenant_logos (
  tenant_id     uuid PRIMARY KEY DEFAULT current_tenant() REFERENCES tenants(id),
  content_type  text CHECK (content_type IN ('image/png', 'image/jpeg', 'image/webp')),
  data          bytea CHECK (data IS NULL OR octet_length(data) BETWEEN 1 AND 10485760),
  sha256        text CHECK (sha256 IS NULL OR sha256 ~ '^[0-9a-f]{64}$'),
  width         integer CHECK (width IS NULL OR width BETWEEN 1 AND 20000),
  height        integer CHECK (height IS NULL OR height BETWEEN 1 AND 20000),
  version       integer NOT NULL DEFAULT 1,
  updated_by    text NOT NULL,
  updated_at    timestamptz NOT NULL DEFAULT now(),
  -- A logo is all of its parts or none (removed).
  CONSTRAINT tenant_logo_whole CHECK ((data IS NULL) = (content_type IS NULL) AND (data IS NULL) = (sha256 IS NULL)
                                      AND (data IS NULL) = (width IS NULL) AND (data IS NULL) = (height IS NULL))
);

-- The database checks the stored digest against the bytes, so a logo is what its hash says.
CREATE FUNCTION tenant_branding_version() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF TG_OP = 'UPDATE' THEN
    IF NEW.tenant_id <> OLD.tenant_id THEN
      RAISE EXCEPTION 'branding belongs to its company' USING ERRCODE = 'check_violation';
    END IF;
    NEW.version := OLD.version + 1;
  ELSE
    NEW.version := 1;
  END IF;
  NEW.updated_at := now();
  RETURN NEW;
END$$;
CREATE TRIGGER tenant_branding_version BEFORE INSERT OR UPDATE ON tenant_branding FOR EACH ROW EXECUTE FUNCTION tenant_branding_version();
CREATE TRIGGER tenant_logos_version BEFORE INSERT OR UPDATE ON tenant_logos FOR EACH ROW EXECUTE FUNCTION tenant_branding_version();

CREATE FUNCTION tenant_logos_digest() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF NEW.data IS NOT NULL AND NEW.sha256 <> encode(sha256(NEW.data), 'hex') THEN
    RAISE EXCEPTION 'the logo digest does not match its bytes' USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END$$;
CREATE TRIGGER tenant_logos_zdigest BEFORE INSERT OR UPDATE ON tenant_logos FOR EACH ROW EXECUTE FUNCTION tenant_logos_digest();

DO $$
DECLARE tbl text;
BEGIN
  FOREACH tbl IN ARRAY ARRAY['tenant_branding', 'tenant_logos'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', tbl);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', tbl);
    EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (tenant_id = current_tenant()) WITH CHECK (tenant_id = current_tenant())', tbl);
    EXECUTE format('CREATE POLICY owner_maintenance ON %I TO %I USING (serk_maintenance_session()) WITH CHECK (serk_maintenance_session())', tbl, current_user);
    EXECUTE format('REVOKE ALL ON %I FROM PUBLIC', tbl);
    EXECUTE format('GRANT SELECT, INSERT ON %I TO serk_app', tbl);
  END LOOP;
END$$;
GRANT UPDATE (brand_name, brand_suffix, eyebrow, motto, description, address, phone, email, website, theme, updated_by) ON tenant_branding TO serk_app;
GRANT UPDATE (content_type, data, sha256, width, height, updated_by) ON tenant_logos TO serk_app;
