package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// AUD-2026-028：分页下推后 writePage 只负责输出本页数据与总数。
// 本文件固定输出边界语义：首页/末页/超界页/空结果/不分页。

func capturePageEnvelope(t *testing.T, items []string, total, page, pageSize int) PageEnvelope {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	writePage(context, items, total, page, pageSize)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	var body struct {
		Data PageEnvelope `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v body=%s", err, recorder.Body.String())
	}
	return body.Data
}

func envelopeItems(envelope PageEnvelope) []string {
	items, _ := envelope.Items.([]any)
	values := make([]string, 0, len(items))
	for _, item := range items {
		value, _ := item.(string)
		values = append(values, value)
	}
	return values
}

func TestWritePageOutputsRepositoryPageAsIs(t *testing.T) {
	envelope := capturePageEnvelope(t, []string{"a", "b"}, 100, 2, 25)
	if got := envelopeItems(envelope); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("repository page must be output verbatim, got %v", got)
	}
	if envelope.Total != 100 || envelope.Page != 2 || envelope.PageSize != 25 {
		t.Fatalf("unexpected envelope: %+v", envelope)
	}

	// 首页：page 缺省归位到 1。
	envelope = capturePageEnvelope(t, []string{"a"}, 3, 0, 1)
	if envelope.Page != 1 || envelope.Total != 3 || len(envelopeItems(envelope)) != 1 {
		t.Fatalf("page 0 must clamp to page 1: %+v", envelope)
	}

	// 超界页：仓储层返回空切片，total 保持不变（前端据此渲染空页）。
	envelope = capturePageEnvelope(t, []string{}, 100, 999, 25)
	if got := envelopeItems(envelope); len(got) != 0 {
		t.Fatalf("over-range page must be empty, got %v", got)
	}
	if envelope.Total != 100 || envelope.Page != 999 || envelope.PageSize != 25 {
		t.Fatalf("over-range envelope must keep total and page: %+v", envelope)
	}

	// 空结果：total 为 0。
	envelope = capturePageEnvelope(t, []string{}, 0, 1, 25)
	if envelope.Total != 0 || len(envelopeItems(envelope)) != 0 {
		t.Fatalf("empty result envelope: %+v", envelope)
	}

	// 不分页：返回全部行，total 以实际长度为准。
	envelope = capturePageEnvelope(t, []string{"a", "b", "c"}, 0, 0, 0)
	if got := envelopeItems(envelope); len(got) != 3 {
		t.Fatalf("unpaged list must return everything, got %v", got)
	}
	if envelope.Total != 3 || envelope.Page != 1 || envelope.PageSize != 3 {
		t.Fatalf("unpaged envelope: %+v", envelope)
	}
}

// pageSlice 仅供尚未下推分页的内存列表（设备台账）使用，语义必须与历史
// writePage 切片一致：首页/中间页/末页/越界页/不分页。
func TestPageSliceMatchesLegacyWritePageClipping(t *testing.T) {
	all := []string{"a", "b", "c", "d", "e"}
	cases := []struct {
		name      string
		source    []string
		page      int
		pageSize  int
		want      []string
		wantTotal int
	}{
		{"first page", all, 1, 2, []string{"a", "b"}, 5},
		{"middle page", all, 2, 2, []string{"c", "d"}, 5},
		{"last partial page", all, 3, 2, []string{"e"}, 5},
		{"over-range page", all, 9, 2, []string{}, 5},
		{"empty source", []string{}, 1, 2, []string{}, 0},
		{"unpaged", all, 2, 0, []string{"a", "b", "c", "d", "e"}, 5},
		{"page zero", all, 0, 2, []string{"a", "b"}, 5},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got, total := pageSlice(test.source, test.page, test.pageSize)
			if total != test.wantTotal {
				t.Fatalf("total = %d, want %d", total, test.wantTotal)
			}
			if len(got) != len(test.want) {
				t.Fatalf("page = %v, want %v", got, test.want)
			}
			for i := range got {
				if got[i] != test.want[i] {
					t.Fatalf("page = %v, want %v", got, test.want)
				}
			}
		})
	}
}
