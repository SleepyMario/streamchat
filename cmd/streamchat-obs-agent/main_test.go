package main

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrivateControl(t *testing.T) {
	a := &agent{token: strings.Repeat("x", 32)}
	r := httptest.NewRequest("POST", "/gps", strings.NewReader(`{"layout":"Full"}`))
	w := httptest.NewRecorder()
	a.api(w, r)
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
	r.Header.Set("Authorization", "Bearer "+a.token)
	w = httptest.NewRecorder()
	a.api(w, r)
	if w.Code != 200 || a.gps != "Full" {
		t.Fatal(w.Code, a.gps)
	}
	r = httptest.NewRequest("POST", "/gps", strings.NewReader(`{"layout":"arbitrary"}`))
	r.Header.Set("Authorization", "Bearer "+a.token)
	w = httptest.NewRecorder()
	a.api(w, r)
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
}
func TestCaptionFiles(t *testing.T) {
	if v := combineCaption("hello", "hello", "你好"); v != "hello\n你好" {
		t.Fatal(v)
	}
	p := filepath.Join(t.TempDir(), "caption.txt")
	for _, v := range []string{"你好", ""} {
		if e := atomicText(p, v); e != nil {
			t.Fatal(e)
		}
		b, e := os.ReadFile(p)
		if e != nil || string(b) != v+"\n" {
			t.Fatal(string(b), e)
		}
	}
}
