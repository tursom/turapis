-- Active: 1774921117665@@47.238.161.178@3306@steammew_master
-- 004: Add request/response capture columns to access_logs

ALTER TABLE access_logs ADD COLUMN client_req TEXT NOT NULL DEFAULT '';
ALTER TABLE access_logs ADD COLUMN client_resp TEXT NOT NULL DEFAULT '';
ALTER TABLE access_logs ADD COLUMN upstream_req TEXT NOT NULL DEFAULT '';
ALTER TABLE access_logs ADD COLUMN upstream_resp TEXT NOT NULL DEFAULT '';
