-- Geolocation no longer uses an ipapi.is account: sources are queried through
-- each proxy, so there is no key, daily budget or provider-wide state.
DELETE FROM settings WHERE key = 'geo.api_key';
DROP TABLE IF EXISTS geo_state;
