package sl

import "testing"

func TestWearableTextures(t *testing.T) {
	const asset = "LLWearable version 22\nAn Example Skin\n\n\tpermissions 0\n\t{\n\t}\nparameters 2\n108 0\n110 .5\ntextures 2\n0 16437e57-7e57-c0de-7cea-1fdd006ae0b5\n5 d2be7e57-7e57-c0de-87a7-bf51dbc108f5\n"
	got, err := WearableTextures([]byte(asset))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Index != 0 || got[1].Index != 5 ||
		got[0].Texture.String() != "16437e57-7e57-c0de-7cea-1fdd006ae0b5" {
		t.Errorf("got %+v", got)
	}
	if got, err := WearableTextures([]byte("LLWearable version 22\nparameters 0\n")); err != nil || got != nil {
		t.Errorf("no textures block: %v, %v", got, err)
	}
	if _, err := WearableTextures([]byte("textures 3\n0 16437e57-7e57-c0de-7cea-1fdd006ae0b5\n")); err == nil {
		t.Error("a short textures block was accepted")
	}
}
