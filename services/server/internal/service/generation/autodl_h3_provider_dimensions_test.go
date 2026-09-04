package generation

import "testing"

func TestAutoDLH3DimensionsUseCloudWorkflowSizes(t *testing.T) {
	tests := []struct {
		name       string
		ratio      string
		resolution string
		wantWidth  int
		wantHeight int
		wantOK     bool
	}{
		{name: "landscape 768p", ratio: "16:9", resolution: "768p", wantWidth: 1344, wantHeight: 768, wantOK: true},
		{name: "portrait 768p", ratio: "9:16", resolution: "768p", wantWidth: 768, wantHeight: 1344, wantOK: true},
		{name: "square 768p", ratio: "1:1", resolution: "768p", wantWidth: 768, wantHeight: 768, wantOK: true},
		{name: "landscape 1080p", ratio: "16:9", resolution: "1080p", wantWidth: 1920, wantHeight: 1080, wantOK: true},
		{name: "portrait 1080p", ratio: "9:16", resolution: "1080p", wantWidth: 1080, wantHeight: 1920, wantOK: true},
		{name: "reject image 2K", ratio: "16:9", resolution: "2K"},
		{name: "reject image 3K", ratio: "16:9", resolution: "3K"},
		{name: "reject legacy 720p", ratio: "16:9", resolution: "720p"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			width, height, ok := autoDLH3Dimensions(map[string]any{
				"aspectRatio": test.ratio,
				"resolution":  test.resolution,
			})
			if width != test.wantWidth || height != test.wantHeight || ok != test.wantOK {
				t.Fatalf("dimensions = %dx%d, %v; want %dx%d, %v", width, height, ok, test.wantWidth, test.wantHeight, test.wantOK)
			}
		})
	}
}
