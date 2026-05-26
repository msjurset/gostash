package identify

import "testing"

// Pin the title-overwrite heuristic. This is the highest-risk
// piece in the worker — too eager and we destroy user edits;
// too conservative and the Photos export titles like
// "IMG_20240515_123456.jpg" stay garbage.
func TestShouldReplaceTitle(t *testing.T) {
	cases := []struct {
		title string
		want  bool
	}{
		// Empty / blank — always replace.
		{"", true},

		// Common auto-title prefixes from cameras and ingest paths.
		{"IMG_20240515_123456.jpg", true},
		{"IMG_1234.heic", true},
		{"DSC_0123.jpg", true},
		{"DSC01234.JPG", true}, // Sony camera roll — DSC family covers underscore-less variants too
		{"PXL_20240515_120000.jpg", true},
		{"Photos-2024-mushrooms.zip-image-3.jpg", true},
		{"Photos 12 May 2024.jpg", true},
		{"Screenshot 2024-05-15.png", true},
		{"stash-google-photos/IMG.jpg", true},

		// Anything that ends in an image/video extension is
		// considered filename-ish.
		{"my mushroom.jpg", true},
		{"random.mp4", true},

		// Human-edited titles — preserved.
		{"Eastern Bluebird in the Apple Tree", false},
		{"Trail map of Bear Mountain", false},
		{"Notes for tomorrow's meeting", false},
	}
	for _, tc := range cases {
		t.Run(tc.title, func(t *testing.T) {
			if got := shouldReplaceTitle(tc.title); got != tc.want {
				t.Errorf("shouldReplaceTitle(%q) = %v, want %v", tc.title, got, tc.want)
			}
		})
	}
}
