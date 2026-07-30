//go:build !linux && !android

package ops

import (
	"context"
	"errors"
)

func startScheduleEventListener(_ context.Context, kind string, _ chan<- scheduleEvent) error {
	return errors.New(kind + " 事件监听仅在 Android/Linux 上可用；任务定义已保留")
}
