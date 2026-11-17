-- How far the current conversion of a recording has got, as a whole percent of its duration, for
-- MPD's processing progress (add-mpd-processing-progress). NULL before a conversion reports any.
ALTER TABLE videos ADD COLUMN conversion_percent SMALLINT CHECK (conversion_percent BETWEEN 0 AND 100);
