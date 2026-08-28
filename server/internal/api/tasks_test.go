package api

import "testing"

func TestValidCron(t *testing.T) {
	valid := []string{
		"* * * * *",          // 每分钟
		"*/5 * * * *",        // 每 5 分钟
		"0 */6 * * *",        // 每 6 小时
		"30 2 * * *",         // 每天 02:30
		"0 0 1 * *",          // 每月 1 号
		"0 9 * * 1-5",        // 工作日 09:00
		"5,10,30 * * * *",    // 逗号列举
		"0-30 * * * *",       // 范围
		"0 0 1,15 * *",       // 多值 + 通配
		"59 23 31 12 0",      // 边界值
	}
	invalid := []string{
		"",                     // 空
		"* * * *",              // 4 段
		"* * * * * *",          // 6 段（任务只收标准 5 段）
		"60 * * * *",           // 分超界
		"* 24 * * *",           // 时超界
		"* * 0 * *",            // 日不能为 0
		"* * 32 * *",           // 日超界
		"* * * 13 *",           // 月超界
		"* * * * 8",            // 周超界
		"5-2 * * * *",          // 范围反向
		"*/0 * * * *",          // step 为 0
		"*/x * * * *",          // step 非数字
		"*/5-10 * * * *",       // step 后不允许范围
		"a b c d e",            // 非数字
		"* * * * * extra",      // 多余字段（被 Fields 拆出 6 段）
	}
	for _, e := range valid {
		if !validCron(e) {
			t.Errorf("validCron(%q) 应为 true", e)
		}
	}
	for _, e := range invalid {
		if validCron(e) {
			t.Errorf("validCron(%q) 应为 false", e)
		}
	}
}
