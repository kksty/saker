package api

import (
	"sync"
	"testing"

	"github.com/kksty/saker/internal/model"
)

func TestProjectSettingsConcurrentRoutingAndBriefing(t *testing.T) {
	s, _ := newBindingServer(t)
	s.SetProjectSettings("scope-a", "description-a")
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := 0; i < 1000; i++ {
			name := "a"
			if i%2 != 0 {
				name = "b"
			}
			s.SetProjectSettings("scope-"+name, "description-"+name)
		}
	})
	wg.Go(func() {
		for i := 0; i < 1000; i++ {
			docs := s.CommanderDocs()
			if docs != "## 约束\ndescription-a\n\n## 起点\nscope-a\n\n" && docs != "## 约束\ndescription-b\n\n## 起点\nscope-b\n\n" {
				t.Errorf("mixed document snapshot: %q", docs)
				return
			}
			if _, _, err := s.planRun(model.Intent{ID: "fixture"}, ""); err != nil {
				t.Error(err)
				return
			}
			_ = s.scopeText()
		}
	})
	wg.Wait()
}
