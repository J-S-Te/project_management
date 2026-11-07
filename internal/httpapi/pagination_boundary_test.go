package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// 列表接口的分页边界（P0 回退后）：省略 page_size（或传 <=0）时返回全部行，
// 只有显式传正数 page_size 才分页；显式分页保留单页上限与 page 上限；
// all 快照模式与 truncated 标记独立保留。
// AUD-2026-028：分页下推后 writePage 只负责输出本页数据与总数。

func newQueryContext(t *testing.T, rawQuery string) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest(http.MethodGet, "/?"+rawQuery, nil)
	return context
}

// TestPageParamsBoundedDefaults 固定分页参数的解析契约：省略 page_size（或传 <=0）
// 表示不分页（PageSize=0，返回全部行），显式正数才分页并受上界约束，
// all 快照模式固定 500 行且与分页参数互斥。
func TestPageParamsBoundedDefaults(t *testing.T) {
	cases := []struct {
		name    string
		query   string
		want    pageQuery
		wantErr bool
	}{
		{"omitted parameters stay unpaged", "", pageQuery{Page: 1, PageSize: 0}, false},
		{"explicit page without page size stays unpaged", "page=3", pageQuery{Page: 1, PageSize: 0}, false},
		{"explicit page size", "page_size=50", pageQuery{Page: 1, PageSize: 50}, false},
		{"page and page size together", "page=2&page_size=25", pageQuery{Page: 2, PageSize: 25}, false},
		{"page size capped at maximum", "page_size=100000", pageQuery{Page: 1, PageSize: maximumPageSize}, false},
		{"page at upper bound accepted", "page=100000&page_size=20", pageQuery{Page: 100000, PageSize: 20}, false},
		{"page above upper bound rejected when paged", "page=100001&page_size=20", pageQuery{}, true},
		{"page above upper bound ignored when unpaged", "page=100001", pageQuery{Page: 1, PageSize: 0}, false},
		{"page zero rejected", "page=0", pageQuery{}, true},
		{"page negative rejected", "page=-1", pageQuery{}, true},
		{"page size zero means unpaged", "page_size=0", pageQuery{Page: 1, PageSize: 0}, false},
		{"page size negative means unpaged", "page_size=-10", pageQuery{Page: 1, PageSize: 0}, false},
		{"page size non numeric rejected", "page_size=abc", pageQuery{}, true},
		{"page non numeric rejected", "page=1.5", pageQuery{}, true},
		{"snapshot flag 1", "all=1", pageQuery{Page: 1, PageSize: maximumSnapshotRows, All: true}, false},
		{"snapshot flag true", "all=true", pageQuery{Page: 1, PageSize: maximumSnapshotRows, All: true}, false},
		{"snapshot flag is case insensitive", "all=TRUE", pageQuery{Page: 1, PageSize: maximumSnapshotRows, All: true}, false},
		{"snapshot flag with page is ambiguous", "all=1&page=2", pageQuery{}, true},
		{"snapshot flag with page size is ambiguous", "all=1&page_size=20", pageQuery{}, true},
		{"snapshot flag 0 is rejected", "all=0", pageQuery{}, true},
		{"snapshot flag yes is rejected", "all=yes", pageQuery{}, true},
		{"empty snapshot flag is ignored", "all=", pageQuery{Page: 1, PageSize: 0}, false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got, err := pageParams(newQueryContext(t, test.query))
			if test.wantErr {
				if err == nil {
					t.Fatalf("pageParams(%q) = %+v, want validation error", test.query, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("pageParams(%q) unexpected error: %v", test.query, err)
			}
			if got != test.want {
				t.Fatalf("pageParams(%q) = %+v, want %+v", test.query, got, test.want)
			}
		})
	}
}

func capturePageEnvelope(t *testing.T, items []string, total int, query pageQuery) PageEnvelope {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	writePage(context, items, total, query)
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
	envelope := capturePageEnvelope(t, []string{"a", "b"}, 100, pageQuery{Page: 2, PageSize: 25})
	if got := envelopeItems(envelope); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("repository page must be output verbatim, got %v", got)
	}
	if envelope.Total != 100 || envelope.Page != 2 || envelope.PageSize != 25 {
		t.Fatalf("unexpected envelope: %+v", envelope)
	}
	if envelope.Truncated {
		t.Fatalf("regular paging must not be flagged as truncated: %+v", envelope)
	}

	// 首页：page 缺省归位到 1。
	envelope = capturePageEnvelope(t, []string{"a"}, 3, pageQuery{Page: 0, PageSize: 1})
	if envelope.Page != 1 || envelope.Total != 3 || len(envelopeItems(envelope)) != 1 {
		t.Fatalf("page 0 must clamp to page 1: %+v", envelope)
	}

	// 超界页：仓储层返回空切片，total 保持不变（前端据此渲染空页）。
	envelope = capturePageEnvelope(t, []string{}, 100, pageQuery{Page: 999, PageSize: 25})
	if got := envelopeItems(envelope); len(got) != 0 {
		t.Fatalf("over-range page must be empty, got %v", got)
	}
	if envelope.Total != 100 || envelope.Page != 999 || envelope.PageSize != 25 {
		t.Fatalf("over-range envelope must keep total and page: %+v", envelope)
	}

	// 空结果：total 为 0。
	envelope = capturePageEnvelope(t, []string{}, 0, pageQuery{Page: 1, PageSize: 25})
	if envelope.Total != 0 || len(envelopeItems(envelope)) != 0 || envelope.Truncated {
		t.Fatalf("empty result envelope: %+v", envelope)
	}

	// pageSize 缺失表示不分页：items 就是完整集合，envelope 回显 page=1 与
	// page_size=len(items)，total 以实际切片长度为准（与改动前的兼容语义一致）。
	envelope = capturePageEnvelope(t, []string{"a", "b", "c"}, 3, pageQuery{})
	if envelope.Page != 1 || envelope.PageSize != 3 || envelope.Total != 3 || envelope.Truncated {
		t.Fatalf("missing page size must return the full list: %+v", envelope)
	}
}

// TestWritePageFlagsTruncatedSnapshot 固定 all 快照模式的 truncated 标记：
// 只有快照模式且 total 大于实际返回行数时为 true，普通分页保持 false。
func TestWritePageFlagsTruncatedSnapshot(t *testing.T) {
	items := make([]string, maximumSnapshotRows)
	for index := range items {
		items[index] = "row"
	}
	envelope := capturePageEnvelope(t, items, 1200, pageQuery{Page: 1, PageSize: maximumSnapshotRows, All: true})
	if !envelope.Truncated {
		t.Fatalf("snapshot of %d rows out of 1200 must be truncated: %+v", len(items), envelope)
	}
	if envelope.Total != 1200 || envelope.Page != 1 || envelope.PageSize != maximumSnapshotRows {
		t.Fatalf("snapshot envelope must report the full total and the snapshot cap: %+v", envelope)
	}

	envelope = capturePageEnvelope(t, []string{"a", "b"}, 2, pageQuery{Page: 1, PageSize: maximumSnapshotRows, All: true})
	if envelope.Truncated {
		t.Fatalf("complete snapshot must not be truncated: %+v", envelope)
	}

	// 普通分页里"本页少于 total"是正常现象，不能被误标成截断。
	envelope = capturePageEnvelope(t, []string{"a", "b"}, 100, pageQuery{Page: 1, PageSize: 2})
	if envelope.Truncated {
		t.Fatalf("regular paging must not set truncated: %+v", envelope)
	}
}

// pageSlice 仅供尚未下推分页的内存列表（设备台账）使用，语义必须与 writePage
// 一致：首页/中间页/末页/越界页；page_size 缺失（<=0）表示不分页、原样返回全部行。
func TestPageSliceMatchesWritePageClipping(t *testing.T) {
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
		{"missing page size returns everything", all, 1, 0, []string{"a", "b", "c", "d", "e"}, 5},
		{"missing page size ignores page", all, 2, 0, []string{"a", "b", "c", "d", "e"}, 5},
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
