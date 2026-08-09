// Package watcher 监视 tasks/ 目录，文件变化即重建任务索引（派生数据）
package watcher

import (
	"log"
	"time"

	"github.com/fsnotify/fsnotify"

	"control-api/internal/store"
	"control-api/internal/tasks"
)

const debounce = 500 * time.Millisecond

// Sync 全量同步一次（索引为派生物，全量重建最简单可靠）
func Sync(dir string, st *store.Store) error {
	metas, err := tasks.Scan(dir)
	if err != nil {
		return err
	}
	for _, m := range metas {
		if err := st.UpsertTask(m); err != nil {
			log.Printf("[watcher] upsert %s: %v", m.TaskID, err)
		}
	}
	if len(metas) > 0 {
		log.Printf("[watcher] 已同步 %d 个任务", len(metas))
	}
	return nil
}

// Watch 启动 fsnotify 监听（debounce：连续事件合并为一次同步）
func Watch(dir string, st *store.Store, done <-chan struct{}) error {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	if err := w.Add(dir); err != nil {
		w.Close()
		return err
	}
	go func() {
		defer w.Close()
		var timer <-chan time.Time
		for {
			select {
			case <-done:
				return
			case _, ok := <-w.Events:
				if !ok {
					return
				}
				timer = time.After(debounce)
			case err, ok := <-w.Errors:
				if ok {
					log.Printf("[watcher] %v", err)
				}
			case <-timer:
				if err := Sync(dir, st); err != nil {
					log.Printf("[watcher] sync: %v", err)
				}
				timer = nil
			}
		}
	}()
	return nil
}
