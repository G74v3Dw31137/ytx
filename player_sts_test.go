package ytx

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestSTSLexicalCandidates(t *testing.T) {
	const valid = `;var actual={signatureTimestamp:20760};`
	for _, tc := range []struct{ name, prefix string }{
		{"line-comment", "// {signatureTimestamp:11111}\n"},
		{"line-comment-cr", "// {sts:11111}\r"},
		{"line-comment-unicode", "// {sts:11111}\u2028"},
		{"block-comment", `/* {signatureTimestamp:11111} */`},
		{"double-string", `var fake="{signatureTimestamp:11111}";`},
		{"single-string", `var fake='{signatureTimestamp:11111}';`},
		{"escaped-double-quote", `var fake="\" ,signatureTimestamp:11111} ";`},
		{"escaped-single-quote", `var fake='\' ,sts:11111} ';`},
		{"escaped-backslashes", `var fake="\\\" ,sts:11111} \\";`},
		{"even-backslashes", `var fake="\\";`},
		{"string-line-continuation", "var fake='\\\r\n,sts:11111}';"},
		{"template-text", "var fake=`{sts:11111}`;"},
		{"template-escape", "var fake=`\\` {sts:11111} \\${ {sts:11111} }`;"},
		{"template-expression", "var fake=`${({signatureTimestamp:11111})}`;"},
		{"template-nested", "var fake=`${ `nested ${ ({sts:11111}) }` }`;"},
		{"template-expression-trivia", "var fake=`${ /* } ` */ /[}]/.test('}') ? ({sts:11111}) : 0 }`;"},
		{"regexp", `var fake=/{signatureTimestamp:11111}/;`},
		{"regexp-escape-class", `var fake=/[\/] {sts:11111}/;`},
		{"regexp-control", `if (x) /,sts:11111}/.test(x);`},
		{"regexp-async-control", `for await (x of y) /,sts:11111,tail/.test(x);`},
		{"regexp-default", `export default /,sts:11111,tail/.test(x);`},
		{"regexp-return", `function f(){return /,sts:11111}/;}`},
		{"division-member-keyword", `x . return / "fake/ ,sts:11111,tail" / y;`},
		{"division-member-control", `x . if (a) / "fake/ ,sts:11111,tail" / y;`},
		{"division", `var ratio=a/b/c;`},
		{"identifier-prefix", `var fake={mysts:11111,asignatureTimestamp:11111};`},
		{"identifier-suffix", `var fake={stsMore:11111,signatureTimestamp$:11111};`},
		{"unicode-identifier", `var fake={éSts:11111,stsé:11111};`},
		{"escaped-identifier", `var fake={\u0073ts:11111,sts\u0061:11111};`},
		{"member-assignment", `x.sts=11111;x.signatureTimestamp=11111;`},
		{"block-label", `function f(){sts:11111} if(x){signatureTimestamp:11111}`},
		{"switch-label", `switch(x){case y,sts:11111}`},
		{"method-label", `var fake={method(){sts:11111}};`},
		{"computed-key", `var fake={['sts']:11111};`},
		{"quoted-other-key", `var fake={'prefix sts':11111,"sts suffix":11111};`},
		{"html-comments", "<!-- {sts:11111}\n--> {sts:11111}\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, source := range []string{tc.prefix + valid, valid + tc.prefix} {
				got, err := extractSTS([]byte(source))
				if err != nil || got != 20760 {
					t.Fatalf("STS = %d, %v", got, err)
				}
			}
			if _, err := extractSTS([]byte(tc.prefix)); !errors.Is(err, ErrPlayerScript) {
				t.Fatalf("fake alone: error = %v", err)
			}
		})
	}
}

func TestSTSPropertiesAndSelection(t *testing.T) {
	for _, source := range []string{
		`x={signatureTimestamp:20760}`,
		`x={"signatureTimestamp":20760}`,
		`x={'signatureTimestamp':20760}`,
		`x={sts:20760}`,
		`x={"sts":20760}`,
		`x={'sts':20760}`,
		`x={other:1,sts:20760,tail:2}`,
		`x={sts:20760};y={signatureTimestamp:20760}`,
		`x={sts:20760,sts:20760,"signatureTimestamp":20760}`,
		`x={nested:{sts:20760},signatureTimestamp:20760}`,
		`x={sts /* key */ : /* value */ 20760 /* end */ }`,
		"x = {\r\n\tsts\v:\f20760\n}",
		"x={\u00a0sts\uFEFF:\u202820760\u2029}",
		`function f(){return {sts:20760}}`,
		`x=[{sts:20760}]`,
		`f({sts:20760})`,
		"#!/usr/bin/env node ,sts:11111,tail\nx={sts:20760}",
	} {
		t.Run(source, func(t *testing.T) {
			got, err := extractSTS([]byte(source))
			if err != nil || got != 20760 {
				t.Fatalf("STS = %d, %v", got, err)
			}
		})
	}
	if got, err := extractSTS([]byte(`x={sts:2147483647}`)); err != nil || got != 2147483647 {
		t.Fatalf("max int32: STS = %d, %v", got, err)
	}
	source, err := os.ReadFile("testdata/player-sts.js")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := extractSTS(source); err != nil || got != 20760 {
		t.Fatalf("STS = %d, %v", got, err)
	}
}

func TestSTSRejectsUnsupportedCandidates(t *testing.T) {
	for _, value := range []string{
		"0", "00", "020760", "-20760", "+20760", "20760.0", ".5", "1e5", "1E+5", "0x5118", "0b10", "0o12",
		"20_760", "20760n", "20760oops", "20760\\u0061", "2147483648", "999999999999999999999999999999",
		`"20760"`, "'20760'", "`20760`", "(20760)", "20760+1", "20760/1", "20760<<1", "20760?1:2", "getSTS()",
		"20760 /* no delimiter */ other", "20760 1", "20760.1.2", "NaN", "Infinity",
	} {
		t.Run(value, func(t *testing.T) {
			for _, suffix := range []string{"", `;y={signatureTimestamp:20760}`} {
				if _, err := extractSTS([]byte(`x={sts:` + value + `}` + suffix)); !errors.Is(err, ErrPlayerScript) {
					t.Fatalf("accepted %q: %v", value, err)
				}
			}
		})
	}
	for _, source := range []string{
		"", `var missing=true`, `x={othersts:20760}`, `x={sts:20760`, `x={sts:`, `x={sts:20760};y={sts`,
		"#!/usr/bin/env node ,sts:11111,tail\n",
		`x={sts:11111};y={signatureTimestamp:20760}`, `x={signatureTimestamp:20760};y={sts:11111}`,
		`x={sts:11111,signatureTimestamp:20760}`, `x={sts:20760,sts:11111}`,
		`x={nested:{sts:11111},signatureTimestamp:20760}`,
		`x={sts:20760}; /* unterminated`, `x={sts:20760}; "unterminated`,
		"x={sts:20760}; 'raw\nnewline'", "x={sts:20760}; `unterminated",
		"x={sts:20760}; `${({sts:11111})", `x={sts:20760}; /unterminated`,
		`if(x){} /,sts:11111}/;x={sts:20760}`, // Block/object slash ambiguity fails closed.
		`x={sts:20760};if(x){} /,sts:11111}/`, // A credible prefix does not bypass suffix checks.
		"label:while(x)break label\n /,sts:11111,tail/.test(x);x={sts:20760}",
		`await /,sts:11111,tail/;x={sts:20760}`,
		`yield /,sts:11111,tail/;x={sts:20760}`,
		`for(x of /,sts:11111,tail/){};x={sts:20760}`,
		`x=a+{sts:11111};y={sts:20760}`, // Unsupported object context cannot hide a conflict.
		`x=()=>{sts:20760}`, `function f(){return` + "\n" + `{sts:20760}}`,
	} {
		t.Run(source, func(t *testing.T) {
			if _, err := extractSTS([]byte(source)); !errors.Is(err, ErrPlayerScript) {
				t.Fatalf("accepted unsupported source: %v", err)
			}
		})
	}
}

func TestSTSBounds(t *testing.T) {
	const valid = `;x={signatureTimestamp:20760};`
	for _, prefix := range []string{
		"//" + strings.Repeat("x", maxMetadataBytes-len(valid)-3) + "\n",
		`var fake="` + strings.Repeat("x", maxMetadataBytes-len(valid)-len(`var fake="";`)) + `";`,
		"var fake=`" + strings.Repeat("x", maxMetadataBytes-len(valid)-len("var fake=``;")) + "`;",
	} {
		source := []byte(prefix + valid)
		if len(source) != maxMetadataBytes {
			t.Fatal("test input must meet the exact byte bound")
		}
		got, err := extractSTS(source)
		if err != nil || got != 20760 {
			t.Fatalf("at byte bound: STS = %d, %v", got, err)
		}
		if _, err := extractSTS(append(source, ' ')); !errors.Is(err, ErrPlayerScript) {
			t.Fatalf("over byte bound: %v", err)
		}
	}
	for _, source := range []string{
		strings.Repeat("(", maxSTSNesting+1) + valid,
		strings.Repeat("[", maxSTSNesting+1) + valid,
		strings.Repeat("{", maxSTSNesting+1) + valid,
		strings.Repeat("` ${", maxSTSNesting+1) + "0" + strings.Repeat("} `", maxSTSNesting+1) + valid,
		"`${" + strings.Repeat("{", maxSTSNesting+1) + valid,
	} {
		if _, err := extractSTS([]byte(source)); !errors.Is(err, ErrPlayerScript) {
			t.Fatalf("over nesting bound: %v", err)
		}
	}
	for _, source := range []string{
		strings.Repeat("[", maxSTSNesting-1) + `{sts:20760}` + strings.Repeat("]", maxSTSNesting-1),
		strings.Repeat("` ${", maxSTSNesting) + "0" + strings.Repeat("} `", maxSTSNesting) + valid,
	} {
		if got, err := extractSTS([]byte(source)); err != nil || got != 20760 {
			t.Fatalf("at nesting bound: STS = %d, %v", got, err)
		}
	}
	// Many equal candidates must remain linear and must all be checked.
	source := bytes.Repeat([]byte(`x={sts:20760};`), 100000)
	if got, err := extractSTS(source); err != nil || got != 20760 {
		t.Fatalf("many candidates: STS = %d, %v", got, err)
	}
	if _, err := extractSTS(append(source, []byte(`x={sts:11111}`)...)); !errors.Is(err, ErrPlayerScript) {
		t.Fatalf("late conflict: %v", err)
	}
}
