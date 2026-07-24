package process

import (
	"reflect"
	"testing"
)

func TestSanitizeChildArgs_SpaceDelimitedPort(t *testing.T) {
	args := []string{"--model", "x.gguf", "--port", "8080"}
	got := SanitizeChildArgs(args)
	want := []string{"--model", "x.gguf"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestSanitizeChildArgs_EqualsDelimitedPort(t *testing.T) {
	args := []string{"--model", "x.gguf", "--port=8080"}
	got := SanitizeChildArgs(args)
	want := []string{"--model", "x.gguf"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestSanitizeChildArgs_NoPort(t *testing.T) {
	args := []string{"--model", "x.gguf"}
	got := SanitizeChildArgs(args)
	want := []string{"--model", "x.gguf"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestFreePort(t *testing.T) {
	port, err := FreePort()
	if err != nil {
		t.Fatalf("FreePort error: %v", err)
	}
	if port <= 0 || port > 65535 {
		t.Errorf("port %d out of range", port)
	}
}
