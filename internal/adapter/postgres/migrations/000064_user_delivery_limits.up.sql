BEGIN;
-- Per-user overrides of the direct delivery limits (G07.4). NULL follows the
-- server-wide setting; zero exempts the user from that limit. Overrides only
-- apply while the matching limit is enabled in configuration (G45.4).
ALTER TABLE users
 ADD COLUMN max_streams integer CONSTRAINT users_max_streams_check CHECK(max_streams BETWEEN 0 AND 128),
 ADD COLUMN max_kbps bigint CONSTRAINT users_max_kbps_check CHECK(max_kbps BETWEEN 0 AND 10000000);
COMMIT;
