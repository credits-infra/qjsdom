package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"testing"
)

// TestReorderArgsMovesFlagsAfterPositionals 钉住 README 承诺的行为:
// flags 可以写在位置参数之后(`qjsdom <url> -json` 里的 -json 不能被丢掉)。
func TestReorderArgsMovesFlagsAfterPositionals(t *testing.T) {
	cases := []struct {
		名  string
		输入 []string
		想  []string
	}{
		{
			"布尔 flag 写在 URL 后面",
			[]string{"https://example.com", "-json"},
			[]string{"-json", "https://example.com"},
		},
		{
			"取值 flag 写在 URL 后面,值必须跟着它",
			[]string{"https://example.com", "-max", "100"},
			[]string{"-max", "100", "https://example.com"},
		},
		{
			"取值 flag 的值不是数字时也一样(不能被当成 URL)",
			[]string{"https://example.com", "-allow-schemes", "file"},
			[]string{"-allow-schemes", "file", "https://example.com"},
		},
		{
			"自带等号的写法不动它的值",
			[]string{"https://example.com", "-max=100"},
			[]string{"-max=100", "https://example.com"},
		},
		{
			"-- 分隔符必须保留(它之后的位置参数不许被当 flag 吃掉)",
			[]string{"-json", "--", "-max"},
			[]string{"-json", "--", "-max"},
		},
		{
			"-- 之前的位置参数照旧被挪到后面",
			[]string{"https://example.com", "--", "-max"},
			[]string{"--", "https://example.com", "-max"},
		},
	}
	for _, c := range cases {
		got := reorderArgs(c.输入)
		if len(got) != len(c.想) {
			t.Errorf("%s: reorderArgs(%v) = %v,想要 %v", c.名, c.输入, got, c.想)
			continue
		}
		for i := range c.想 {
			if got[i] != c.想[i] {
				t.Errorf("%s: reorderArgs(%v) = %v,想要 %v", c.名, c.输入, got, c.想)
				break
			}
		}
	}
}

// TestReorderArgsKnowsEveryValueFlag 把「新增取值 flag 却忘了登记」这类
// 漏洞变成测试失败,而不是等到命令行上才暴露。
//
// 为什么需要它:reorderArgs 靠一张手写的 valueFlags 表判断"flag 后面那个
// token 是它的值还是位置参数"。漏登记时,值会被当成位置参数(实测:加了
// -allow-schemes 但没登记,`qjsdom file:///x.html -allow-schemes file`
// 报的是「协议 "" 未放行」—— 因为 URL 变成了 "file")。
//
// 做法:直接解析本文件的 flag 声明,按类型判断哪些 flag 需要值,再逐个
// 喂给 reorderArgs,看它有没有把紧随其后的 token 当成值。这样以后新增
// flag 无需改这张测试,它自己会跟着源码走。
func TestReorderArgsKnowsEveryValueFlag(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatalf("解析 main.go 失败: %v", err)
	}

	// flag.String/Int/Duration/... 需要值;flag.Bool 不需要。
	取值的函数 := map[string]bool{
		"String": true, "Int": true, "Int64": true, "Duration": true,
		"Float64": true, "Uint": true,
	}
	声明的 := map[string]bool{} // flag 名 -> 是否取值
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) < 2 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "flag" {
			return true
		}
		取值 := 取值的函数[sel.Sel.Name]
		if !取值 && sel.Sel.Name != "Bool" {
			return true // Var/Text 等暂不在此列
		}
		名, err := strconv.Unquote(call.Args[0].(*ast.BasicLit).Value)
		if err != nil {
			return true
		}
		声明的[名] = 取值
		return true
	})

	if len(声明的) < 10 {
		t.Fatalf("只解析出 %d 个 flag,解析逻辑可能失效了", len(声明的))
	}

	for 名, 取值 := range 声明的 {
		flag参数 := "-" + 名
		got := reorderArgs([]string{"https://example.com", flag参数, "值"})
		if 取值 {
			// 值必须紧跟 flag:漏登记时它会掉到位置参数区
			if len(got) != 3 || got[0] != flag参数 || got[1] != "值" {
				t.Errorf("取值 flag %s 没被 reorderArgs 认出来(值被当成位置参数): %v\n"+
					"修法:把它加进 reorderArgs 里的 valueFlags", flag参数, got)
			}
			continue
		}
		// 布尔 flag 不该吃掉后面那个 token
		if len(got) != 3 || got[0] != flag参数 || got[1] != "https://example.com" || got[2] != "值" {
			t.Errorf("布尔 flag %s 被误当成取值 flag: %v", flag参数, got)
		}
	}
}
