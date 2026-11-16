-- 0096_role_responsibilities.sql — a company role states its responsibilities, not only its permissions.
--
-- Permissions say what the system lets a holder do; responsibilities say what the company expects of them
-- (duties, reporting line, what they answer for). They are free text up to 4,000 characters, changed like the
-- rest of the role (role.manage, fresh second factor, audited, versioned by custom_roles_guard).

ALTER TABLE custom_roles ADD COLUMN responsibilities text NOT NULL DEFAULT '' CHECK (length(responsibilities) <= 4000);
GRANT UPDATE (responsibilities) ON custom_roles TO serk_app;
