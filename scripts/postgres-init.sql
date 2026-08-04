CREATE ROLE workouts_api LOGIN NOBYPASSRLS;
CREATE ROLE workouts_worker LOGIN NOBYPASSRLS;
CREATE ROLE workouts_security_owner NOLOGIN NOBYPASSRLS;
GRANT workouts_security_owner TO workouts_migration;
