package mysql

import (
	"os"
	"strings"
	"testing"
)

// 回归护栏：撤销团队/执行分配不得清空 required_codes。
// 该列承载服务项的必检能力基线（来自检测类别配置）；一旦被清空，重新分配时
// 应用层只能拿到客户端追加要求，资质校验可被绕过（把任务分给缺少必备资质的人员）。
func TestDeliveryRepoNeverClearsRequiredCodesOnRevoke(t *testing.T) {
	content, err := os.ReadFile("delivery.go")
	if err != nil {
		t.Fatalf("read delivery.go: %v", err)
	}
	source := string(content)
	forbidden := "updates[\"required_codes\"] = jsonValue([]string{})"
	if strings.Contains(source, forbidden) {
		t.Fatal("delivery.go must not clear required_codes on revoke events: it is the service item capability baseline")
	}
	required := "updates[\"required_codes\"] = jsonValue(event.Payload[\"required_codes\"])"
	if !strings.Contains(source, required) {
		t.Fatal("assignment events must still persist required_codes from the payload")
	}
}
