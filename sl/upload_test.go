package sl

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/quark-idlemind/slgo/msg"
)

// j2c is a 512x512 texture as far as anything here can tell.
var j2c = codestream(512, 512)

// codestream builds the head of a JPEG 2000 codestream: SOC, then the
// SIZ marker carrying the image size and its origin.  Nothing here
// decodes one, and nothing here has an encoder -- what the upload path
// reads is the size, and this is where the size lives.
func codestream(w, h int) []byte {
	b := []byte{0xff, 0x4f, 0xff, 0x51, 0x00, 0x2f, 0x00, 0x00}
	for _, v := range []int{w, h, 0, 0, w, h, 0, 0} {
		b = append(b, byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
	}
	return append(b, "and then the image"...)
}

// completeUpload is what the capability says when it has taken the
// bytes and made an item.
func completeUpload(item, asset msg.UUID) string {
	return `<llsd><map>` +
		`<key>state</key><string>complete</string>` +
		`<key>new_asset</key><string>` + asset.String() + `</string>` +
		`<key>new_inventory_item</key><string>` + item.String() + `</string>` +
		`</map></llsd>`
}

// TestUploadingATextureReadsTheItemBack: the capability answers with two
// ids and no item, and an id is not evidence that anything went looking
// for the item would find it.  So the upload is not finished until the
// folder has been read and the item is in it.
func TestUploadingATextureReadsTheItemBack(t *testing.T) {
	w, f := newFakeSession(t)
	up := serveUpload(t, f, UploadCap, func() (int, string) {
		return 200, completeUpload(theChild, theOther)
	})
	f.ServeInventory(t, func(folder msg.UUID) []*Item {
		if folder != thePrim {
			return nil
		}
		return []*Item{{ID: theChild, ParentID: thePrim, AssetID: theOther, Name: "a texture"}}
	})

	it, res, err := w.UploadTexture(context.Background(), "a texture", "why it is here", thePrim, j2c)
	if err != nil {
		t.Fatalf("UploadTexture: %v", err)
	}
	if it.ID != theChild || it.AssetID != theOther {
		t.Errorf("item = %s asset %s, want %s and %s", it.ID, it.AssetID, theChild, theOther)
	}
	if res.NewItem != theChild || res.State != "complete" {
		t.Errorf("result = %+v", res)
	}

	// The bytes go to the second server, unaltered.
	if got := <-up.body; !bytes.Equal(got, j2c) {
		t.Errorf("uploaded %d bytes, want the %d it was given", len(got), len(j2c))
	}

	// What the capability was told.  folder_id has to be a uuid rather
	// than a string: the live service refuses the same characters sent
	// as a string, which is why llsd.UUID exists.
	asked := string(<-up.asked)
	for _, want := range []string{
		"<uuid>" + thePrim.String() + "</uuid>",
		"<string>texture</string>",
		fmt.Sprintf("<integer>%d</integer>", PermAll),
		fmt.Sprintf("<integer>%d</integer>", UploadCost),
	} {
		if !strings.Contains(asked, want) {
			t.Errorf("the capability was not told %s:\n%s", want, asked)
		}
	}
}

// TestUploadingANonTextureIsRefusedBeforeItIsPaidFor: the capability
// takes any bytes at all and charges for them, so a PNG uploaded as a
// texture costs money and produces a texture no viewer can decode.  It
// has to fail here, before anything is sent.
func TestUploadingANonTextureIsRefusedBeforeItIsPaidFor(t *testing.T) {
	w, f := newFakeSession(t)
	up := serveUpload(t, f, UploadCap, func() (int, string) {
		return 200, completeUpload(theChild, theOther)
	})

	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}
	_, _, err := w.UploadTexture(context.Background(), "not a texture", "", thePrim, png)
	if err == nil {
		t.Fatal("a PNG was uploaded as a texture")
	}
	if !strings.Contains(err.Error(), "JPEG 2000") {
		t.Errorf("error = %v, want it to say what was wrong with the bytes", err)
	}
	select {
	case b := <-up.asked:
		t.Errorf("the capability was asked anyway: %s", b)
	default:
	}
}

// TestATextureTheGridWouldRefuseIsRefusedHere: the grid checks the
// dimensions AFTER it has taken the bytes and charged for them, so
// every rule it enforces is enforced here first.  The messages are its
// own, measured against the beta grid.
func TestATextureTheGridWouldRefuseIsRefusedHere(t *testing.T) {
	cases := []struct {
		what string
		w, h int
		want string
	}{
		{"a width that is not a power of two", 300, 256, "not a power of two"},
		{"a height that is not a power of two", 256, 200, "not a power of two"},
		{"too wide", 4096, 256, "larger than"},
		{"too tall", 256, 4096, "larger than"},
		{"the largest the grid allows", 2048, 2048, ""},
		{"the smallest there is", 1, 1, ""},
		{"any power of two by any other", 1024, 64, ""},
	}
	for _, c := range cases {
		w, h, err := TextureDims(codestream(c.w, c.h))
		if w != c.w || h != c.h {
			t.Errorf("%s: read %dx%d, want %dx%d", c.what, w, h, c.w, c.h)
		}
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: %dx%d refused: %v", c.what, c.w, c.h, err)
		case c.want != "" && err == nil:
			t.Errorf("%s: %dx%d accepted, and the grid would not have", c.what, c.w, c.h)
		case c.want != "" && !strings.Contains(err.Error(), c.want):
			t.Errorf("%s: error = %v, want it to say %q", c.what, err, c.want)
		}
	}
}

// TestABigTextureCostsMore: above a megapixel the fee is a different
// one, and naming the wrong figure is refused outright -- "The server
// expects a different upload fee" -- so the size has to choose it.
func TestABigTextureCostsMore(t *testing.T) {
	w, f := newFakeSession(t)
	up := serveUpload(t, f, UploadCap, func() (int, string) {
		return 200, completeUpload(theChild, theOther)
	})
	f.ServeInventory(t, func(msg.UUID) []*Item {
		return []*Item{{ID: theChild, ParentID: thePrim, AssetID: theOther}}
	})

	for _, c := range []struct {
		w, h int
		want int
	}{
		{1024, 1024, UploadCost},      // a megapixel exactly is still the flat fee
		{2048, 1024, LargeUploadCost}, // one pixel more is not
		{2048, 2048, LargeUploadCost},
	} {
		_, _, err := w.UploadTexture(context.Background(), "a texture", "", thePrim, codestream(c.w, c.h))
		if err != nil {
			t.Fatalf("%dx%d: %v", c.w, c.h, err)
		}
		<-up.body
		asked := string(<-up.asked)
		want := fmt.Sprintf("<key>expected_upload_cost</key><integer>%d</integer>", c.want)
		if !strings.Contains(asked, want) {
			t.Errorf("%dx%d offered the wrong fee, wanted L$%d:\n%s", c.w, c.h, c.want, asked)
		}
	}
}

// TestTextureDimIsTheViewersRounding, which is biased downwards: the
// bandwidth saved is worth more than the detail lost, so a dimension
// only goes up when it is more than 1.75 times the power below it.
func TestTextureDimIsTheViewersRounding(t *testing.T) {
	for _, c := range []struct{ in, want int }{
		{1, 4}, {2, 4}, {3, 4}, {4, 4}, // the viewer's floor, though the grid takes 1x1
		{100, 64},    // 100/64 is 1.56, so down
		{115, 128},   // 115/64 is 1.80, so up
		{512, 512},   // already there
		{1000, 1024}, // 1.95 of 512, so up
		{1800, 2048}, // 1.76 of 1024, just over the line
		{5000, 2048}, // clamped
	} {
		if got := TextureDim(c.in); got != c.want {
			t.Errorf("TextureDim(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

// TestAnUploadRefusalSaysWhy: a refusal arrives as 200 and a state of
// "error", so the reason is in the body rather than in the status.  Not
// reading it turns "you have no money" into "no uploader".
func TestAnUploadRefusalSaysWhy(t *testing.T) {
	w, f := newFakeSession(t)
	f.ServeCap(t, UploadCap, func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "application/llsd+xml")
		fmt.Fprint(rw, `<llsd><map>`+
			`<key>state</key><string>error</string>`+
			`<key>message</key><string>Insufficient funds to upload</string>`+
			`</map></llsd>`)
	})

	_, _, err := w.UploadTexture(context.Background(), "a texture", "", thePrim, j2c)
	if err == nil {
		t.Fatal("an upload the grid refused was reported as done")
	}
	if !strings.Contains(err.Error(), "Insufficient funds") {
		t.Errorf("error = %v, want the grid's own reason", err)
	}
}

// TestUploadingAKindThatHasNoUploadPath: the types an upload can make
// are a short list, and a caller naming another one is told so rather
// than having a word guessed for it.
func TestUploadingAKindThatHasNoUploadPath(t *testing.T) {
	w, _ := newFakeSession(t)

	_, _, err := w.UploadAsset(context.Background(), Upload{
		Name: "notes", Type: AssetNotecard, Folder: thePrim,
	}, []byte("hello"))
	if err == nil || !strings.Contains(err.Error(), "InvType") {
		t.Errorf("error = %v, want it to name the field that would fix it", err)
	}
}

// TestAnUploadNeedsSomethingToUpload guards the two ways of asking for
// nothing, since both reach the capability and one of them is charged.
func TestAnUploadNeedsSomethingToUpload(t *testing.T) {
	w, _ := newFakeSession(t)
	ctx := context.Background()

	if _, _, err := w.UploadTexture(ctx, "", "", thePrim, j2c); err == nil {
		t.Error("an upload with no name was accepted")
	}
	if _, _, err := w.UploadTexture(ctx, "a texture", "", thePrim, nil); err == nil {
		t.Error("an upload with no bytes was accepted")
	}
}

// TestUploadingWithoutAFolderUsesTheSystemOne: the grid would file it
// itself, but then nothing knows where to read it back from, so the
// folder is looked up and named.
func TestUploadingWithoutAFolderUsesTheSystemOne(t *testing.T) {
	w, f := newFakeSession(t)
	textures := msg.MustParseUUID("17037e57-7e57-c0de-b12a-24470e312844")
	up := serveUpload(t, f, UploadCap, func() (int, string) {
		return 200, completeUpload(theChild, theOther)
	})
	f.ServeInventoryTree(t, func(folder msg.UUID) ([]*Folder, []*Item) {
		if folder == textures {
			return nil, []*Item{{ID: theChild, ParentID: textures, AssetID: theOther, Name: "a texture"}}
		}
		return []*Folder{{ID: textures, Name: "Textures"}}, nil
	})

	it, _, err := w.UploadTexture(context.Background(), "a texture", "", msg.UUID{}, j2c)
	if err != nil {
		t.Fatalf("UploadTexture: %v", err)
	}
	if it.ParentID != textures {
		t.Errorf("item landed in %s, want the Textures folder %s", it.ParentID, textures)
	}
	if asked := string(<-up.asked); !strings.Contains(asked, "<uuid>"+textures.String()+"</uuid>") {
		t.Errorf("the capability was not told which folder:\n%s", asked)
	}
}
