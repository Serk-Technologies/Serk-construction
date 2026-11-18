-- Proposed for the schema compatibility plan (ADR 0026); NOT part of the Go migration history and applied only
-- to isolated test databases until the owner approves a cutover plan.
--
-- serk_rational: an exact rational stored as its canonical text "n" or "n/d". The database, not the
-- application, refuses every non-canonical spelling: leading zeros, "-0", a denominator of 1, a negative or zero
-- denominator, or an unreduced fraction (gcd(n, d) <> 1). Equal values therefore have equal text, so equality,
-- uniqueness and hashing work on the column directly; ordering uses serk_rational_cmp.
CREATE FUNCTION serk_rational_ok(v text) RETURNS boolean
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE SET search_path = pg_catalog, pg_temp AS $$
  SELECT length(v) <= 39460 -- Rational.MaxCanonicalLength: the kernel's 65,536-bit precision budget
     AND v ~ '^(0|-?[1-9][0-9]*)(/[1-9][0-9]*)?$'
     AND (position('/' in v) = 0
          OR (split_part(v, '/', 2) <> '1'
              AND split_part(v, '/', 1) <> '0'
              AND gcd(split_part(v, '/', 1)::numeric, split_part(v, '/', 2)::numeric) = 1))
$$;

CREATE DOMAIN serk_rational AS text CHECK (serk_rational_ok(VALUE));

-- Exact comparison: n1·d2 vs n2·d1 in numeric (arbitrary precision).
CREATE FUNCTION serk_rational_cmp(a serk_rational, b serk_rational) RETURNS integer
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE SET search_path = pg_catalog, pg_temp AS $$
  SELECT sign(split_part(a, '/', 1)::numeric * coalesce(nullif(split_part(b, '/', 2), ''), '1')::numeric
            - split_part(b, '/', 1)::numeric * coalesce(nullif(split_part(a, '/', 2), ''), '1')::numeric)::integer
$$;
