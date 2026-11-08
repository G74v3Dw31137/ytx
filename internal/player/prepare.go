package player

import (
	"errors"
	"reflect"
	"strings"

	"github.com/dop251/goja/ast"
	"github.com/dop251/goja/parser"
)

// Narrow adaptation of yt-dlp/ejs modifyPlayer and extract/createSolver.
// See THIRD_PARTY.md and notices/ejs.txt. Unknown wrappers fail closed.
func nodeText(source string, n ast.Node) string { return source[int(n.Idx0())-1 : int(n.Idx1())-1] }
func markedURLFactory(fn *ast.FunctionLiteral) bool {
	if fn.Async || fn.Generator || fn.ParameterList == nil || len(fn.ParameterList.List) != 3 || fn.ParameterList.Rest != nil {
		return false
	}
	for _, s := range fn.Body.List {
		ex, ok := s.(*ast.ExpressionStatement)
		if !ok {
			continue
		}
		call, ok := ex.Expression.(*ast.CallExpression)
		if !ok || len(call.ArgumentList) != 2 {
			continue
		}
		member, ok := call.Callee.(*ast.DotExpression)
		if !ok {
			continue
		}
		if _, ok = member.Left.(*ast.Identifier); !ok {
			continue
		}
		a, ok := call.ArgumentList[0].(*ast.StringLiteral)
		if !ok || a.Value.String() != "alr" {
			continue
		}
		b, ok := call.ArgumentList[1].(*ast.StringLiteral)
		if ok && b.Value.String() == "yes" {
			// The marker must refer to the object returned by the factory.
			id := member.Left.(*ast.Identifier)
			last, valid := fn.Body.List[len(fn.Body.List)-1].(*ast.ReturnStatement)
			if !valid {
				return false
			}
			returned, valid := last.Argument.(*ast.Identifier)
			return valid && returned.Name == id.Name
		}
	}
	return false
}
func preparePlayer(source string) (prepared string, err error) {
	defer func() {
		if recover() != nil {
			prepared = ""
			err = ErrUnsupported
		}
	}()

	program, e := parser.ParseFile(nil, "player.js", source, 0)
	if e != nil {
		return "", errors.Join(ErrUnsupported, errors.New("player JS parse failed"))
	}
	if len(program.Body) != 2 {
		return "", errors.Join(ErrUnsupported, errors.New("unsupported player wrapper: expected two statements"))
	}
	// Support the demonstrated namespace/IIFE wrapper explicitly. Unknown wrapper
	// declarations or argument shapes must not be silently discarded.
	namespace, ok := program.Body[0].(*ast.VariableStatement)
	if !ok || len(namespace.List) != 1 {
		return "", ErrUnsupported
	}
	binding := namespace.List[0]
	name, ok := binding.Target.(*ast.Identifier)
	if !ok || name.Name.String() != "_yt_player" {
		return "", ErrUnsupported
	}
	object, ok := binding.Initializer.(*ast.ObjectLiteral)
	if !ok || len(object.Value) != 0 {
		return "", ErrUnsupported
	}
	ex, ok := program.Body[1].(*ast.ExpressionStatement)
	if !ok {
		return "", errors.Join(ErrUnsupported, errors.New("unsupported player wrapper"))
	}
	call, ok := ex.Expression.(*ast.CallExpression)
	if !ok {
		return "", errors.Join(ErrUnsupported, errors.New("unsupported player call"))
	}
	if len(call.ArgumentList) != 1 {
		return "", ErrUnsupported
	}
	argument, ok := call.ArgumentList[0].(*ast.Identifier)
	if !ok || argument.Name.String() != "_yt_player" {
		return "", ErrUnsupported
	}
	outer, ok := call.Callee.(*ast.FunctionLiteral)
	if !ok || outer.Async || outer.Generator || len(outer.Body.List) < 2 {
		return "", errors.Join(ErrUnsupported, errors.New("unsupported player function"))
	}
	if outer.ParameterList == nil || len(outer.ParameterList.List) != 1 || outer.ParameterList.Rest != nil {
		return "", ErrUnsupported
	}
	parameter, ok := outer.ParameterList.List[0].Target.(*ast.Identifier)
	if !ok || parameter.Name.String() != "g" {
		return "", ErrUnsupported
	}
	var body strings.Builder
	var factories []string
	cursor := int(outer.Body.LeftBrace)
	for i, s := range outer.Body.List {
		begin := skipSourceSpace(source, cursor)
		end := statementEnd(source, int(s.Idx1())-1)
		cursor = end
		if i == 0 {
			if nodeText(source, s) != "var window=this" {
				return "", errors.Join(ErrUnsupported, errors.New("unsupported player window alias"))
			}
			continue
		}
		if exp, ok := s.(*ast.ExpressionStatement); ok {
			if call, ok := exp.Expression.(*ast.CallExpression); ok {
				if member, ok := call.Callee.(*ast.DotExpression); ok {
					if root, ok := member.Left.(*ast.Identifier); ok && (root.Name.String() == "window" || root.Name.String() == "document" || root.Name.String() == "navigator") {
						return "", errors.Join(ErrUnsupported, errors.New("unsupported browser startup"))
					}
				}
			}
			switch v := exp.Expression.(type) {
			case *ast.AssignExpression:
				if fn, ok := v.Right.(*ast.FunctionLiteral); ok && markedURLFactory(fn) {
					if _, ok := v.Left.(*ast.Identifier); !ok {
						return "", ErrUnsupported
					}
					factories = append(factories, nodeText(source, v.Left))
				}
			}
		}
		// Retain initialization in source order.
		if begin < 0 || end < begin || end > len(source) {
			return "", errors.Join(ErrUnsupported, errors.New("unsupported player AST span"))
		}
		body.WriteString(source[begin:end])
		body.WriteString(";\n")
	}
	if len(factories) > 1 {
		return "", errors.Join(ErrUnsupported, ErrAmbiguous)
	}
	if len(factories) == 0 {
		return "", ErrUnsupported
	}
	setup := `var XMLHttpRequest={prototype:{}};var location={hash:"",host:"www.youtube.com",hostname:"www.youtube.com",href:"https://www.youtube.com/watch?v=poc",origin:"https://www.youtube.com",password:"",pathname:"/watch",port:"",protocol:"https:",search:"?v=poc",username:""};var document=Object.create(null),navigator=Object.create(null),self=globalThis,window=globalThis;`
	// Discover once at initialization, before any transform input is supplied.
	// Require the factory's URL protocol and exactly one distinct callable method.
	// Unknown additional properties are rejected, including noncallable decoys.
	footer := `var __probe=(` + factories[0] + `)("https://www.youtube.com/watch?v=poc","s",undefined);
var __proto=Object.getPrototypeOf(__probe);
if(!__proto || typeof __probe.get!=="function" || typeof __probe.set!=="function")throw "ytx:unsupported";
var __keys=Array.from(new Set(Object.keys(__proto).concat(Object.getOwnPropertyNames(__proto)))).filter(function(k){return !["constructor","set","get","clone"].includes(k)});
if(__keys.length!==1)throw "ytx:ambiguous";
var __method=__keys[0];if(typeof __probe[__method]!=="function")throw "ytx:unsupported";
globalThis.__extractMethodSource=Function.prototype.toString.call(__probe[__method]);
globalThis.__extractTransform=function(sig,n){var u=(` + factories[0] + `)("https://www.youtube.com/watch?v=poc","s",sig?encodeURIComponent(sig):undefined);u.set("n",n);u[__method]();var s=u.get("s");return {sig:typeof s==="string"&&s!==""?decodeURIComponent(s):null,n:u.get("n")??null}};`

	return setup + `var _yt_player={};(function(g){` + body.String() + footer + `})(_yt_player);`, nil
}

// Goja's AST positions omit parentheses around expressions. Only scan the tail
// beyond the parser-provided end; strings/regex inside the statement stay intact.
func skipSourceSpace(s string, i int) int {
	for i < len(s) {
		switch s[i] {
		case ' ', '\t', '\r', '\n':
			i++
			continue
		}
		if strings.HasPrefix(s[i:], "//") {
			for i < len(s) && s[i] != '\n' {
				i++
			}
			continue
		}
		if strings.HasPrefix(s[i:], "/*") {
			j := strings.Index(s[i+2:], "*/")
			if j < 0 {
				return len(s)
			}
			i += j + 4
			continue
		}
		break
	}
	return i
}
func statementEnd(s string, end int) int {
	for {
		next := skipSourceSpace(s, end)
		if next >= len(s) {
			return end
		}
		if s[next] == ')' {
			end = next + 1
			continue
		}
		if s[next] == ';' {
			return next + 1
		}
		return end
	}
}

// validMethodShape validates the uniquely discovered zero-argument URL method.
// Both function-valued properties and class-method source syntax are supported.
// Getter/setter protocol callability is checked during runtime initialization.
func validMethodShape(source string) bool {
	if len(source) == 0 || len(source) > 64<<10 {
		return false
	}
	text := "(" + source + ")"
	if !strings.HasPrefix(source, "function") {
		text = "({" + source + "})"
	}
	program, e := parser.ParseFile(nil, "method.js", text, 0)
	if e != nil || len(program.Body) != 1 {
		return false
	}
	ex, ok := program.Body[0].(*ast.ExpressionStatement)
	if !ok {
		return false
	}
	var fn *ast.FunctionLiteral
	switch node := ex.Expression.(type) {
	case *ast.FunctionLiteral:
		fn = node
	case *ast.ObjectLiteral:
		if len(node.Value) != 1 {
			return false
		}
		p, ok := node.Value[0].(*ast.PropertyKeyed)
		if !ok {
			return false
		}
		fn, _ = p.Value.(*ast.FunctionLiteral)
	}
	if fn == nil || fn.Async || fn.Generator || fn.ParameterList == nil || len(fn.ParameterList.List) != 0 || fn.ParameterList.Rest != nil || len(fn.Body.List) == 0 {
		return false
	}
	last, ok := fn.Body.List[len(fn.Body.List)-1].(*ast.ReturnStatement)
	if !ok || last.Argument == nil {
		return false
	}
	// Require a call or write through the receiver in the method body. This is a structural
	// compatibility boundary, not a general proof of future player semantics.
	found := false
	var visit func(reflect.Value)
	visit = func(v reflect.Value) {
		if !v.IsValid() || found {
			return
		}
		if v.Kind() == reflect.Interface {
			if !v.IsNil() {
				visit(v.Elem())
			}
			return
		}
		if v.Kind() == reflect.Pointer {
			if v.IsNil() {
				return
			}
			if v.CanInterface() {
				switch n := v.Interface().(type) {
				case *ast.FunctionLiteral, *ast.ArrowFunctionLiteral:
					return
				case *ast.CallExpression:
					if receiverMember(n.Callee) && !getterCall(n.Callee) {
						found = true
						return
					}
				case *ast.AssignExpression:
					if receiverMember(n.Left) {
						found = true
						return
					}
				}
			}
			visit(v.Elem())
			return
		}
		switch v.Kind() {
		case reflect.Struct:
			for _, field := range v.Fields() {
				visit(field)
			}
		case reflect.Slice:
			for i := 0; i < v.Len(); i++ {
				visit(v.Index(i))
			}
		}
	}
	visit(reflect.ValueOf(fn.Body))
	return found
}

func receiverMember(e ast.Expression) bool {
	switch n := e.(type) {
	case *ast.ThisExpression:
		return true
	case *ast.DotExpression:
		return receiverMember(n.Left)
	case *ast.BracketExpression:
		return receiverMember(n.Left)
	}
	return false
}

// A protocol read alone cannot identify a transformation method.
func getterCall(e ast.Expression) bool {
	switch n := e.(type) {
	case *ast.DotExpression:
		return n.Identifier.Name.String() == "get"
	case *ast.BracketExpression:
		if key, ok := n.Member.(*ast.StringLiteral); ok {
			return key.Value.String() == "get"
		}
	}
	return false
}
