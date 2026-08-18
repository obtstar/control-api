// Package watcher 监视 tasks/ 目录，文件变化即重建任务索引（派生数据）
package watcher

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"

	"control-api/internal/store"
	"control-api/internal/tasks"
)

const (
	debounce = 500 * time.Millisecond
	// maxWait 防抖上限：持续高频事件最迟按此周期同步一次，
	// 不被事件流无限推迟（FINDING-037）
	maxWait = 5 * time.Second
)

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
	return watch(dir, st, done, nil)
}

// watch 同 Watch；onSync 在每次防抖同步后回调（测试观察点，生产为 nil）。
// fsnotify 非递归（FINDING-006）：启动时把 tasks/ 下已有子目录一并纳入，
// 事件循环中对新建目录动态 Add、对删除/改名目录清理监听。
func watch(dir string, st *store.Store, done <-chan struct{}, onSync func()) error {
	return watchD(dir, st, done, onSync, debounce, maxWait)
}

// watchD 同 watch，防抖窗口与上限可注入（测试用小参数）
func watchD(dir string, st *store.Store, done <-chan struct{}, onSync func(),
	d, mw time.Duration) error {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("new watcher: %w", err)
	}
	watched := map[string]bool{}
	add := func(p string) error {
		if watched[p] {
			return nil // 幂等：重复 Add 同一目录直接跳过
		}
		if err := w.Add(p); err != nil {
			return fmt.Errorf("watch %s: %w", p, err)
		}
		watched[p] = true
		return nil
	}
	if err := add(dir); err != nil {
		w.Close()
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		w.Close()
		return fmt.Errorf("scan %s: %w", dir, err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if err := add(filepath.Join(dir, e.Name())); err != nil {
			w.Close()
			return err
		}
	}
	go loop(w, dir, st, done, watched, add, onSync, d, mw)
	return nil
}

// loop 事件循环：目录增删维护监听集合，任何事件重置防抖计时器；
// capTimer 为防抖上限（FINDING-037），事件流持续时最迟 mw 同步一次
func loop(w *fsnotify.Watcher, dir string, st *store.Store, done <-chan struct{},
	watched map[string]bool, add func(string) error, onSync func(), d, mw time.Duration) {
	defer w.Close()
	var timer, capTimer <-chan time.Time
	sync := func() {
		if err := Sync(dir, st); err != nil {
			log.Printf("[watcher] sync: %v", err)
		}
		if onSync != nil {
			onSync()
		}
		timer, capTimer = nil, nil
	}
	for {
		select {
		case <-done:
			return
		case ev, ok := <-w.Events:
			if !ok {
				return
			}
			handleEvent(w, ev, watched, add)
			timer = time.After(d)
			if capTimer == nil {
				capTimer = time.After(mw)
			}
		case err, ok := <-w.Errors:
			if ok {
				log.Printf("[watcher] %v", err)
			}
		case <-timer:
			sync()
		case <-capTimer:
			sync()
		}
	}
}

// handleEvent 维护目录监听集合：新建目录纳入，删除/改名目录摘除
func handleEvent(w *fsnotify.Watcher, ev fsnotify.Event, watched map[string]bool, add func(string) error) {
	if ev.Has(fsnotify.Create) {
		if fi, err := os.Stat(ev.Name); err == nil && fi.IsDir() {
			if err := add(ev.Name); err != nil {
				log.Printf("[watcher] %v", err)
			}
		}
	}
	if (ev.Has(fsnotify.Remove) || ev.Has(fsnotify.Rename)) && watched[ev.Name] {
		// 目录被删时内核已自动摘除 inotify watch，Remove 报错属正常，忽略
		w.Remove(ev.Name)
		delete(watched, ev.Name)
	}
}
