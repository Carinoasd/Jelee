BEGIN;
-- G40.7: the dominant color joins the optional average color. Both are
-- 0xRRGGBB sampled from the decoded image when its bytes are read (upload,
-- embedded artwork, refresh) and, like every content attribute, exist only
-- while the row describes stored content.
ALTER TABLE item_images ADD COLUMN dominant_color integer CHECK(dominant_color BETWEEN 0 AND 16777215);
ALTER TABLE item_images ADD CONSTRAINT item_images_dominant_color_content CHECK(dominant_color IS NULL OR content_sha256 IS NOT NULL);
COMMIT;
