-- The summary mpd-api sends once staff approve it for the patient. It belongs to
-- the media version it was written for, so new media hides it until mpd-api
-- sends another.
ALTER TABLE mpd_video_state ADD COLUMN patient_summary JSONB;
ALTER TABLE mpd_video_state ADD COLUMN patient_summary_media_version INTEGER;
ALTER TABLE mpd_video_state ADD CONSTRAINT mpd_patient_summary_version
  CHECK ((patient_summary IS NULL) = (patient_summary_media_version IS NULL));
ALTER TABLE mpd_adapter_receipts DROP CONSTRAINT mpd_adapter_receipts_operation_check;
ALTER TABLE mpd_adapter_receipts ADD CONSTRAINT mpd_adapter_receipts_operation_check
  CHECK (operation IN ('password','publication','patient_summary'));
