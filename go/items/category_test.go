package items

import "testing"

func TestNormalizeItemCategory(t *testing.T) {
	cases := []struct {
		raw          string
		wantOK       bool
		wantStorage  string
		wantSingular string
	}{
		{"pet", true, storageKeyPet, "pet"},
		{"PETS", true, storageKeyPet, "pet"},
		{"class", true, storageKeyClass, "class"},
		{"classes", true, storageKeyClass, "class"},
		{"background", true, storageKeyBackground, "background"},
		{"backgrounds", true, storageKeyBackground, "background"},
		{"piece_style", true, storageKeyPieceStyle, "piece_style"},
		{"piece_styles", true, storageKeyPieceStyle, "piece_style"},
		{"piece_theme", false, "", ""},
		{"styles", false, "", ""},
		{"lootbox", false, "", ""},
		{"currency", false, "", ""},
	}

	for _, tc := range cases {
		spec, ok := NormalizeItemCategory(tc.raw)
		if ok != tc.wantOK {
			t.Fatalf("NormalizeItemCategory(%q) ok=%v, want %v", tc.raw, ok, tc.wantOK)
		}
		if ok {
			if spec.StorageKey != tc.wantStorage || spec.SingularType != tc.wantSingular {
				t.Fatalf("NormalizeItemCategory(%q) = %+v, want storage=%q singular=%q", tc.raw, spec, tc.wantStorage, tc.wantSingular)
			}
			if spec.DefaultID(GetStarterPack()) == ^uint32(0) {
				t.Fatalf("NormalizeItemCategory(%q) returned invalid default ID", tc.raw)
			}
		}
	}
}
