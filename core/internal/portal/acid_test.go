package portal

import "testing"

func TestAnACIDIsReadOutOfAnAddress(t *testing.T) {
	for _, testCase := range []struct {
		url  string
		want string
	}{
		{"http://10.0.0.55/srun_portal_pc?ac_id=8&theme=pro", "8"},
		{"http://10.0.0.55/index?acid=12", "12"},
		{"http://10.0.0.55/index?ac_id=", ""},
		{"http://10.0.0.55/index", ""},
		// A value that is not an identifier is not one, however it arrived.
		{"http://10.0.0.55/index?ac_id=" + "a b", ""},
		{"http://10.0.0.55/index?ac_id=%3Cscript%3E", ""},
		{"", ""},
	} {
		if got := ACIDFromURL(testCase.url); got != testCase.want {
			t.Errorf("ACIDFromURL(%q) = %q, want %q", testCase.url, got, testCase.want)
		}
	}
}

// The hidden field the page itself would submit outranks a mention of the name
// somewhere else on it.
func TestTheHiddenFieldIsPreferredToAMention(t *testing.T) {
	page := []byte(`<html><body>
	  <p>ac_id = 99 is what our other campus uses</p>
	  <form><input type="hidden" name="ac_id" value="5"></form>
	</body></html>`)
	if got := ACIDFromHTML(page); got != "5" {
		t.Errorf("ACIDFromHTML = %q, want the form's own value", got)
	}
}

func TestAnACIDIsReadOutOfAPage(t *testing.T) {
	for name, testCase := range map[string]struct {
		body string
		want string
	}{
		"attributes reversed": {`<input value="7" name="ac_id">`, "7"},
		"javascript":          {`var ac_id = "3";`, "3"},
		"assignment":          {`{ac_id: 11, foo: 1}`, "11"},
		"query in a script":   {`location = "/portal?ac_id=4&x=1"`, "4"},
		"nothing":             {`<html><body>welcome</body></html>`, ""},
		"not an identifier":   {`<input name="ac_id" value="<b>">`, ""},
	} {
		t.Run(name, func(t *testing.T) {
			if got := ACIDFromHTML([]byte(testCase.body)); got != testCase.want {
				t.Errorf("ACIDFromHTML = %q, want %q", got, testCase.want)
			}
		})
	}
}

// A page in GBK is still readable for this purpose: every pattern is ASCII, so
// the bytes around the Chinese text do not change the answer.
func TestANonUTF8PageStillYieldsItsACID(t *testing.T) {
	// "欢迎" in GBK, which is not valid UTF-8.
	body := append([]byte("<html><head><title>"), 0xBB, 0xB6, 0xD3, 0xAD)
	body = append(body, []byte(`</title></head><input name="ac_id" value="6">`)...)
	if got := ACIDFromHTML(body); got != "6" {
		t.Errorf("ACIDFromHTML = %q, want 6", got)
	}
}

func TestAPageCanRedirectWithoutAHeader(t *testing.T) {
	for name, testCase := range map[string]struct {
		body string
		want string
	}{
		"script":       {`<script>top.self.location.href="/portal?ac_id=1"</script>`, "/portal?ac_id=1"},
		"plain assign": {`<script>location.href = 'http://1.2.3.4/x'</script>`, "http://1.2.3.4/x"},
		"meta refresh": {`<meta http-equiv="refresh" content="0;url=/go">`, "/go"},
		"nothing":      {`<html></html>`, ""},
	} {
		t.Run(name, func(t *testing.T) {
			if got := RedirectFromHTML([]byte(testCase.body)); got != testCase.want {
				t.Errorf("RedirectFromHTML = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestALocationIsResolvedAgainstThePageItCameFrom(t *testing.T) {
	for _, testCase := range []struct{ base, location, want string }{
		{"http://10.0.0.55/a/b", "/portal", "http://10.0.0.55/portal"},
		{"http://10.0.0.55/a/b", "c", "http://10.0.0.55/a/c"},
		{"http://10.0.0.55/a/b", "https://other.invalid/x", "https://other.invalid/x"},
		{"http://10.0.0.55/a/b", "", ""},
	} {
		if got := Join(testCase.base, testCase.location); got != testCase.want {
			t.Errorf("Join(%q, %q) = %q, want %q",
				testCase.base, testCase.location, got, testCase.want)
		}
	}
}

// base_url is where the gateway's API is, not where its login page is. An
// account that kept the path would POST its credentials to a page that answers
// with HTML.
func TestAnOriginDropsThePageItWasFoundOn(t *testing.T) {
	for _, testCase := range []struct{ raw, want string }{
		{"http://10.0.0.55/srun_portal_pc?ac_id=1", "http://10.0.0.55"},
		{"https://portal.example.edu:8443/a/b", "https://portal.example.edu:8443"},
		{"10.0.0.55", "http://10.0.0.55"},
		{"10.0.0.55/srun_portal", "http://10.0.0.55"},
		{"ftp://10.0.0.55/x", ""},
		{"javascript:alert(1)", ""},
		{"", ""},
	} {
		if got := Origin(testCase.raw); got != testCase.want {
			t.Errorf("Origin(%q) = %q, want %q", testCase.raw, got, testCase.want)
		}
	}
}
