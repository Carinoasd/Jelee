package domain

import "testing"

func TestImageRequestAcceptsEveryG40TypeAndGalleryIndex(t *testing.T) {
	for imageType := range itemImageTypes {
		value, err := NormalizeImageRequest(ImageRequest{Type: imageType})
		if err != nil || value.Type != imageType || value.Index != 0 || value.Format != "jpeg" || value.Width != 640 || value.Height != 640 {
			t.Fatalf("type %s: %+v %v", imageType, value, err)
		}
		_, err = NormalizeImageRequest(ImageRequest{Type: imageType, Index: 1})
		if gallery := imageType == "Backdrop" || imageType == "Chapter"; (err == nil) != gallery {
			t.Fatalf("type %s index 1 accepted=%v", imageType, err == nil)
		}
	}
	value, err := NormalizeImageRequest(ImageRequest{Type: "Fanart", Index: ItemImageMaxIndex, Width: 300})
	if err != nil || value.Type != "Backdrop" || value.Index != ItemImageMaxIndex || value.Width != 300 || value.Height != 0 {
		t.Fatal("Fanart alias", value, err)
	}
	if value, err := NormalizeImageRequest(ImageRequest{}); err != nil || value.Type != "Primary" {
		t.Fatal("default type", err)
	}
	for _, bad := range []ImageRequest{{Type: "Poster"}, {Type: "primary"}, {Type: "Backdrop", Index: -1}, {Type: "Chapter", Index: ItemImageMaxIndex + 1},
		{Type: "Logo", Index: 2}, {Format: "png"}, {Format: "JPEG"}, {Width: 2049}, {Quality: 101},
		{Crop: true, Width: 100}, {Crop: true, Height: 100}, {Crop: true}} {
		if _, err := NormalizeImageRequest(bad); err != ErrInvalid {
			t.Fatalf("accepted %+v", bad)
		}
	}
	if _, err := NormalizeImageRequest(ImageRequest{Format: ImageFormatAVIF}); err != ErrImageUnsupported {
		t.Fatal("avif output must be unsupported", err)
	}
	for _, format := range []string{"", ImageFormatJPEG, ImageFormatWebP} {
		value, err := NormalizeImageRequest(ImageRequest{Format: format, Crop: true, Width: 300, Height: 200})
		if err != nil || !value.Crop || value.Width != 300 || value.Height != 200 || value.Format == "" {
			t.Fatalf("format %q: %+v %v", format, value, err)
		}
	}
}
