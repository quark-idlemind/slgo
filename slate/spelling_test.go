package slate

import "testing"

// TestColorIsReadInEitherSpelling: the language says color, and colour
// is the same word -- for a face, a light and a glTF material -- so a
// script written either way reads the same.
func TestColorIsReadInEitherSpelling(t *testing.T) {
	const h = "slate 1\nobject a is \"A\"\n"
	for _, spelling := range []string{"color", "colour"} {
		s := mustCheck(t, h+
			"expect "+spelling+" a face 0 is 1 0 0.5\n"+
			"expect light a "+spelling+" is 1 0.5 0\n"+
			"expect gltf "+spelling+" a face 0 is 1 0.5 0\n")
		ex := s.Tests[0].Steps[0].Expect
		if len(ex) != 3 {
			t.Fatalf("%s: %d expectations, want 3", spelling, len(ex))
		}
		if ex[0].Colour == nil {
			t.Errorf("expect %s is not a face's color", spelling)
		}
		if ex[1].Light == nil || ex[1].Light.Prop != "color" {
			t.Errorf("light %s is %+v, want the color property", spelling, ex[1].Light)
		}
		if ex[2].GLTF == nil || ex[2].GLTF.Prop != "color" {
			t.Errorf("gltf %s is %+v, want the color property", spelling, ex[2].GLTF)
		}
	}
}

// TestABindingMayStillBeCalledColour: the spelling is folded where a
// keyword is read and nowhere else, so a name is the name it was given.
func TestABindingMayStillBeCalledColour(t *testing.T) {
	s := mustCheck(t, "slate 1\nobject colour is \"A\"\nexpect color colour face 0 is 1 1 1\n")
	if got := s.Objects[0].Name.Text; got != "colour" {
		t.Errorf("the object is called %q, want colour", got)
	}
}
