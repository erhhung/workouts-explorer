package main

import (
	"reflect"
	"testing"
)

func TestSplitLocalities(t *testing.T) {
	got := splitLocalities(" Cupertino,Mountain View, Cupertino ,,Sunnyvale ")
	want := []string{"Cupertino", "Mountain View", "Sunnyvale"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("splitLocalities() = %v, want %v", got, want)
	}
}

func TestEnvInt64(t *testing.T) {
	if envInt64("123", 4) != 123 || envInt64("bad", 4) != 4 || envInt64("", 4) != 4 {
		t.Fatal("unexpected environment integer parsing")
	}
}
