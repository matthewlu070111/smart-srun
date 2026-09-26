package portal

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/transport"
)

func TestRealOptionsPreserveSuffixesAndDoNotInferIdentity(t *testing.T) {
	body := `Contact admin@example.edu<script>var x='<select name="realm"><option value="bad">bad</option></select>';</script>
<select id="service"><option value="1">移动</option><option value="2-@Research.Example.edu" selected>研究网络</option></select>
<select name="realm"><option value="" disabled>请选择</option><option value="??">移动</option>
<option value="@Lab_42">学生 &amp; 教职工</option><option value="">校园网</option><option value="42">编号后缀</option>
<option value="@Lab_42">重复</option><option value="@bad@realm">错误</option><option value="@xn">别名</select>`
	got, err := ReadOperators([]byte(body), "text/html; charset=UTF-8")
	want := []Operator{{"Research.Example.edu", "研究网络"}, {"Lab_42", "学生 & 教职工"}, {"", "校园网"}, {"42", "编号后缀"}, {"xn", "别名"}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v / %v", got, err)
	}
}

func TestOperatorDecodeBoundsExpandedCharsetBeforeAcceptingOptions(t *testing.T) {
	// A valid selector before padding must not make an oversized decoded page
	// acceptable. GBK bytes can fit the wire budget while their UTF-8 output does not.
	body := `<select name="realm"><option value="@stu">学生</option></select>` + strings.Repeat("学", transport.MaxPortalBody/3)
	encoded, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(body))
	if err != nil || len(encoded) >= transport.MaxPortalBody {
		t.Fatalf("fixture does not fit the wire budget: %d %v", len(encoded), err)
	}
	operators, err := ReadOperators(encoded, "text/html; charset=gbk")
	if code, _ := domain.CodeOf(err); code != domain.CodeProtocolInvalid || len(operators) != 0 {
		t.Fatalf("accepted partial operators after charset expansion: %+v %v", operators, err)
	}
	client := environmentFetcher(func(*http.Request) (*http.Response, error) {
		r := page(200, string(encoded), "")
		r.Header.Set("Content-Type", "text/html; charset=gbk")
		return r, nil
	})
	result, err := ProbeOperators(t.Context(), client, "http://portal.invalid/", "")
	if code, _ := domain.CodeOf(err); code != domain.CodeProtocolInvalid || result.OK || len(result.Operators) != 0 {
		t.Fatalf("probe accepted partial operators: %+v %v", result, err)
	}
	operators, err = ReadOperators(nil, "text/html")
	if err != nil || len(operators) != 0 {
		t.Fatalf("empty page invented operators: %+v %v", operators, err)
	}
}

func TestOperatorCharsetHeaderPrecedesMetaAndEntitiesDecodeOnce(t *testing.T) {
	body := `<meta charset="utf-8"><select name="realm"><option value="@stu">学生 &amp;amp; 教职工</option></select>`
	encoded, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	got, err := ReadOperators(encoded, "text/html; Charset=gb2312")
	if err != nil || len(got) != 1 || got[0].Label != "学生 &amp; 教职工" {
		t.Fatalf("%+v / %v", got, err)
	}
	encoded, _ = simplifiedchinese.GBK.NewEncoder().Bytes([]byte(strings.Replace(body, `charset="utf-8"`, `charset="gbk"`, 1)))
	got, err = ReadOperators(encoded, "text/html")
	if err != nil || len(got) != 1 || got[0].Label != "学生 &amp; 教职工" {
		t.Fatalf("meta: %+v / %v", got, err)
	}
}

func TestOperatorsFollowSameOriginAfterACIDAndRefuseActions(t *testing.T) {
	server := portalServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/full": html(`<meta http-equiv="refresh" content="0;url=/landing?ac_id=007&amp;theme=pro">`),
		"/landing": func(w http.ResponseWriter, r *http.Request) {
			if r.URL.RawQuery != "ac_id=007&theme=pro" {
				t.Error("lost full query")
			}
			w.Write([]byte(`<select name="realm"><option value="@Lab_42">实验室</select>`))
		},
	})
	client := newDirect()
	r, err := ProbeOperators(t.Context(), client, server.URL+"/full", "007")
	if err != nil || !r.OK || len(r.Operators) != 1 || r.Operators[0].Suffix != "Lab_42" || client.calls != 2 {
		t.Fatalf("%+v / %v", r, err)
	}
	for _, target := range []string{"http://other.invalid/", "http://portal.invalid:81/", "https://portal.invalid/", "/cgi-bin/srun_portal?action=login", "/%6cogout", "/?action=anything", "http://u:p@portal.invalid/"} {
		calls := 0
		client := environmentFetcher(func(*http.Request) (*http.Response, error) { calls++; return page(302, "", target), nil })
		r, err := ProbeOperators(context.Background(), client, "http://portal.invalid/", "")
		if err != nil || r.OK || calls != 1 {
			t.Fatalf("target %s: %+v / %v calls=%d", target, r, err, calls)
		}
	}
}
