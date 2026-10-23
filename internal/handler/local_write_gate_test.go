package handler

import (
	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLocalWriteGateHoldsBackupAgainstMutationsAndRechecksStop(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	gate := NewLocalWriteGate()
	router := gin.New()
	router.Use(gate.Middleware())
	started, release, next := make(chan struct{}), make(chan struct{}), make(chan struct{}, 1)
	router.POST("/backup", func(c *gin.Context) { close(started); <-release; gate.Stop(); c.Status(200) })
	router.POST("/write", func(c *gin.Context) { next <- struct{}{}; c.Status(200) })
	router.GET("/read", func(c *gin.Context) { c.Status(200) })
	firstDone := make(chan int, 1)
	go func() {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("POST", "/backup", nil))
		firstDone <- w.Code
	}()
	<-started
	secondDone := make(chan int, 1)
	go func() {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("POST", "/write", nil))
		secondDone <- w.Code
	}()
	select {
	case <-next:
		t.Fatal("mutation entered while backup held write gate")
	case <-time.After(30 * time.Millisecond):
	}
	read := httptest.NewRecorder()
	router.ServeHTTP(read, httptest.NewRequest("GET", "/read", nil))
	if read.Code != http.StatusOK {
		t.Fatal("ordinary read blocked during backup")
	}
	close(release)
	if <-firstDone != 200 {
		t.Fatal("backup failed")
	}
	if <-secondDone != 503 {
		t.Fatal("queued write used pre-restore admission")
	}
	select {
	case <-next:
		t.Fatal("stopped old writer entered")
	default:
	}
	gate.Resume()
	write := httptest.NewRecorder()
	router.ServeHTTP(write, httptest.NewRequest("POST", "/write", nil))
	if write.Code != 200 {
		t.Fatal("failed switch could not resume old instance")
	}
}
