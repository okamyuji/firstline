package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func severities(fs []finding) []string {
	out := []string{}
	for _, f := range fs {
		out = append(out, f.severity)
	}
	return out
}

func TestLintLine(t *testing.T) {
	tests := []struct {
		name string
		line string
		want []string
	}{
		{"正しい表記は通す", "DBはSQLiteを使います。", nil},
		{"インラインコード内の識別子は通す", "ドライバは`modernc.org/sqlite`です。", nil},
		{"URLは通す", "https://example.com/sqlite を参照します。", nil},
		{"URLの後ろの日本語も検査する", "https://example.com/aを参照し、TBDです。", []string{"Critical"}},
		{"リンクの後ろの日本語も検査する", "[文書](https://example.com/sqlite)によればmysqlです。", []string{"High"}},
		{"山括弧のURLの後ろも検査する", "<https://example.com/a>の後のTODOです。", []string{"Critical"}},
		{"山括弧の直後の誤記を検出する", "<https://example.com/a>mysqlです。", []string{"High"}},
		{"山括弧の中の非ASCIIのURLは通す", "<https://例.jp/?q=mysql>を見ます。", nil},
		{"相対リンクの中は通す", "[設計](../sqlite/notes.md)を見ます。", nil},
		{"インラインコードの前後の語はつながない", "foo `x` 日本語です。", nil},
		{"TODOsを検出する", "TODOsを残します。", []string{"Critical"}},
		{"タブの境界を検出する", "日本語\tGoです。", []string{"High"}},
		{"感嘆符の後の境界スペースを検出する", "日本語！ Goです。", []string{"High"}},
		{"括弧の後の文末コロンを検出する", "次のとおり（例）：", []string{"Medium"}},
		{"々の後の文末コロンを検出する", "人々:", []string{"Medium"}},
		{"かぎ括弧の後の文末コロンを検出する", "次のとおり」:", []string{"Medium"}},
		{"半角の閉じ括弧の後の文末コロンを検出する", "次の例(半角):", []string{"Medium"}},
		{"墨付き括弧の後の文末コロンを検出する", "次の表【例】：", []string{"Medium"}},
		{"末尾に空白がある文末コロンを検出する", "次のとおり: ", []string{"Medium"}},
		{"インラインコードの後の文末コロンを検出する", "設定は`/etc/x`:", []string{"Medium"}},
		{"小文字のtbdを検出する", "tbdです。", []string{"Critical"}},
		{"インラインコードの直後のTODOを検出する", "`x`TODOです。", []string{"Critical"}},
		{"インラインコードの直前のTBDを検出する", "TBD`x`です。", []string{"Critical"}},
		{"2つのバッククォートのインラインコードは通す", "``mysql`x``を使います。", nil},
		{"疑問符の後の境界スペースを検出する", "日本語？ Goです。", []string{"High"}},
		{"全角コロンの後の境界スペースを検出する", "日本語： Goです。", []string{"High"}},
		{"英数字の後の全角記号の前の境界スペースを検出する", "Go ？です。", []string{"High"}},
		{"英数字の後のタブを検出する", "Go\tです。", []string{"High"}},
		{"字下げした見出しの境界スペースは通す", "   ## 1 Go の版", nil},
		{"アスタリスクの箇条書きの見出し語は通す", "* 目的 firstlineです。", nil},
		{"助詞が途中にある1語目は見出し語として通す", "- 主は者 Goで書きます。", nil},
		{"英字だけの1語目は見出し語とみなさない", "- Go で書きます。", []string{"High"}},
		{"記号を含む英数字の1語目も見出し語とみなさない", "- v1.2 で書きます。", []string{"High"}},
		{"日本語を含む1語目は見出し語として通す", "- Go版 firstlineです。", nil},
		{"英字を含む日本語の1語目は見出し語として通す", "- 配備Go 日本語です。", nil},
		{"英語だけの行の括弧の後のコロンは通す", "See the example (x):", nil},
		{"英語だけの行のインラインコードの後のコロンは通す", "Use `x`:", nil},
		{"英語だけの小文字の行のコロンは通す", "see foo(x):", nil},
		{"途中にコロンがある日本語の文末コロンを検出する", "開始は10:30 (JST):", []string{"Medium"}},
		{"英字で終わる日本語の文末コロンを検出する", "使う言語はGo:", []string{"Medium"}},
		{"数字で終わる日本語の文末コロンを検出する", "版は2:", []string{"Medium"}},
		{"角括弧で終わる日本語の文末コロンを検出する", "例[x]:", []string{"Medium"}},
		{"リンクで終わる日本語の文末コロンを検出する", "詳細は[設定の文書](https://example.com/a):", []string{"Medium"}},
		{"URLで終わる日本語の文末コロンを検出する", "詳細はhttps://example.com/a:", []string{"Medium"}},
		{"漢字の後の文末コロンを検出する", "手順:", []string{"Medium"}},
		{"カタカナの後の文末コロンを検出する", "次のステップ:", []string{"Medium"}},
		{"半角括弧の後の文末コロンを検出する", "例(x):", []string{"Medium"}},
		{"字下げ4つの行は見出しとみなさない", "    ## 1 Go の版", []string{"High"}},
		{"7つの#は見出しとみなさない", "####### 1 Go の版", []string{"High"}},
		{"2つのバッククォートのコードは最短で閉じる", "``a`` mysql ``b``です。", []string{"High"}},
		{"アスタリスクの区切り線は強調とみなさない", "***", nil},
		{"空白を挟んだ区切り線は強調とみなさない", "* * *", nil},
		{"区切り線でない行の強調は検出する", "これは***強調***です。", []string{"High"}},
		{"アスタリスク2つだけの行は強調として検出する", "**", []string{"High"}},
		{"空白を1つ挟んだ区切り線は強調とみなさない", "** *", nil},
		{"字下げした区切り線は強調とみなさない", "   ***", nil},
		{"区切り線で始まる強調の行は検出する", "***強調**です。", []string{"High"}},
		{"半角括弧で囲んだURLの後の文末コロンを検出する", "詳細(https://example.com/a):", []string{"Medium"}},
		{"半角括弧で囲んだURLの後の全角の文末コロンを検出する", "詳細は(https://example.com/a)：", []string{"Medium"}},
		{"読点の後のURLの文末コロンを検出する", "参照、https://example.com/a:", []string{"Medium"}},
		{"空白の後のURLの文末コロンを検出する", "日本語 https://example.com/a:", []string{"Medium"}},
		{"空白の後の山括弧のURLの文末コロンを検出する", "詳しくは <https://example.com/a>:", []string{"Medium"}},
		{"英語だけの行のURLの後のコロンは通す", "See https://example.com/a:", nil},
		{"山形括弧の後の文末コロンを検出する", "次の例〈注〉:", []string{"Medium"}},
		{"数字で終わるURLの後の境界は通す", "参照はhttps://example.com/9 です。", nil},
		{"チルダを含むURLは最後まで通す", "参照はhttps://example.com/~mysql です。", nil},
		{"感嘆符で終わるURLの後の文末コロンを検出する", "詳細はhttps://example.com/a!:", []string{"Medium"}},
		{"セミコロンで終わるURLの後の文末コロンを検出する", "詳細はhttps://example.com/a;:", []string{"Medium"}},
		{"チルダで終わるURLの後の文末コロンを検出する", "詳細はhttps://example.com/a~:", []string{"Medium"}},
		{"感嘆符を含むURLは最後まで通す", "参照はhttps://example.com/!mysql です。", nil},
		{"httpのURLは通す", "参照はhttp://example.com/mysql です。", nil},
		{"httpの山括弧のURLは通す", "参照は<http://example.com/mysql>です。", nil},
		{"空白を含む山括弧はURLとみなさない", "参照は<http://example.com/a mysql>です。", []string{"High"}},
		{"リンクの後の空白と日本語は通す", "[設定](https://example.com/a) Goです。", nil},
		{"英単語の中のTODOは通す", "mastodonを使います。", nil},
		{"英字が前に続くTBDは通す", "xTBDです。", nil},
		{"英字が後ろに続くTODOは通す", "TODOistです。", nil},
		{"URLを文字にしたリンクの直後のTODOを検出する", "詳細は[https://example.com/a](https://example.com/a)TODOです。", []string{"Critical"}},
		{"URLを文字にしたリンクの直後の誤記を検出する", "詳細は[https://example.com/a](https://example.com/a)mysqlです。", []string{"High"}},
		{"2つ目のリンクの前の誤記を検出する", "[a](x)の後のmysql(y)です。", []string{"High"}},
		{"2つの山括弧のURLの間の誤記を検出する", "<https://a.example>のmysql<https://b.example>です。", []string{"High"}},
		{"全角空白で終わる文末コロンを検出する", "手順：\u3000", []string{"Medium"}},
		{"見出し語の後の2つ目の空白を境界スペースとして検出する", "- 目的  Goです。", []string{"High"}},
		{"見出し語の後のタブを境界スペースとして検出する", "- 目的\tGoです。", []string{"High"}},
		{"見出し語の区切りの後のタブを境界スペースとして検出する", "- 目的 \tGoです。", []string{"High"}},
		{"ASCII以外の文字でURLは終わる", "参照はhttps://example.com/émysql です。", []string{"High", "High"}},
		{"行末のアスタリスク3つは区切り線とみなさない", "これは**強調**です***", []string{"High"}},
		{"感嘆符で始まる1語目は見出し語とみなさない", "- !x で書きます。", []string{"High"}},
		{"チルダで始まる1語目は見出し語とみなさない", "- ~x で書きます。", []string{"High"}},
		{"ゔは日本語とみなさない", "ゔ Goです。", nil},
		{"ゝは日本語とみなさない", "ゝ Goです。", nil},
		{"互換漢字は日本語とみなさない", "\uf900 Goです。", nil},
		{"全角英字は英数字とみなさない", "日本語 Ｇです。", nil},
		{"〆の後の文末コロンは検出しない", "〆:", nil},
		{"カタカナの後の境界スペースを検出する", "テスト Goです。", []string{"High"}},
		{"漢字の後の境界スペースを検出する", "設定 Goです。", []string{"High"}},
		{"カタカナの前の境界スペースを検出する", "Go テストです。", []string{"High"}},
		{"漢字の前の境界スペースを検出する", "Go 設定です。", []string{"High"}},
		{"日本語の後の数字の境界スペースを検出する", "日本語 9です。", []string{"High"}},
		{"日本語の後の小文字の境界スペースを検出する", "日本語 abcです。", []string{"High"}},
		{"日本語の後の大文字の境界スペースを検出する", "日本語 ABCです。", []string{"High"}},
		{"数字の後の境界スペースを検出する", "9 です。", []string{"High"}},
		{"小文字の後の境界スペースを検出する", "abc です。", []string{"High"}},
		{"大文字の後の境界スペースを検出する", "ABC です。", []string{"High"}},
		{"ひらがなだけの後の英字の文末コロンを検出する", "これはGo:", []string{"Medium"}},
		{"カタカナの後の英字の文末コロンを検出する", "テストGo:", []string{"Medium"}},
		{"大文字で終わる日本語の文末コロンを検出する", "言語はGO:", []string{"Medium"}},
		{"数字で終わるURLは数字まで消す", "参照はhttps://example.com/1 です。", nil},
		{"英字複合語の一部は通す", "mysqlite3xという名前は対象外です。", nil},
		{"前に英字が続く語は通す", "xsqliteは対象外です。", nil},
		{"後ろに英字が続く語は通す", "sqlite3xは対象外です。", nil},
		{"後ろに数字が続く語は通す", "mysql8は対象外です。", nil},
		{"前にaが続く語は通す", "amysqlは対象外です。", nil},
		{"前にzが続く語は通す", "zmysqlは対象外です。", nil},
		{"前にAが続く語は通す", "Amysqlは対象外です。", nil},
		{"前にZが続く語は通す", "Zmysqlは対象外です。", nil},
		{"後ろにaが続く語は通す", "mysqlaは対象外です。", nil},
		{"後ろにzが続く語は通す", "mysqlzは対象外です。", nil},
		{"後ろに0が続く語は通す", "mysql0は対象外です。", nil},
		{"後ろに9が続く語は通す", "mysql9は対象外です。", nil},
		{"前に記号の続く語は検出する", "@mysqlです。", []string{"High"}},
		{"後ろに記号の続く語は検出する", "mysql/です。", []string{"High"}},
		{"行末の誤記を検出する", "移行先はmysql", []string{"High"}},
		{"小文字のsqlite3を検出する", "DBはsqlite3です。", []string{"High"}},
		{"SQLite3を検出する", "DBはSQLite3です。", []string{"High"}},
		{"golangを検出する", "golangで書きます。", []string{"High"}},
		{"Goは通す", "Goで書きます。", nil},
		{"cloudflareを検出する", "複製先はcloudflareのR2です。", []string{"High"}},
		{"litestreamを検出する", "litestreamで送ります。", []string{"High"}},
		{"Postgresを検出する", "移行先はPostgresです。", []string{"High"}},
		{"postgresqlを検出する", "移行先はpostgresqlです。", []string{"High"}},
		{"PostgreSQLは通す", "移行先はPostgreSQLです。", nil},
		{"mysqlを検出する", "移行先はmysqlです。", []string{"High"}},
		{"MySqlを検出する", "移行先はMySqlです。", []string{"High"}},
		{"MySQLは通す", "移行先はMySQLです。", nil},
		{"HTMXを検出する", "画面はHTMXで作ります。", []string{"High"}},
		{"Htmxを検出する", "画面はHtmxで作ります。", []string{"High"}},
		{"htmxは通す", "画面はhtmxで作ります。", nil},
		{"区切り1文字で並ぶ誤記を両方検出する", "mysql,mysqlの順です。", []string{"High", "High"}},
		{"強調を検出する", "これは**重要**です。", []string{"High"}},
		{"文末コロンを検出する", "手順は次のとおり:", []string{"Medium"}},
		{"全角の文末コロンを検出する", "手順は次のとおり：", []string{"Medium"}},
		{"TBDを検出する", "認証方式はTBDです。", []string{"Critical"}},
		{"TODOを検出する", "TODOを残します。", []string{"Critical"}},
		{"後で決めるを検出する", "方式は後で決める。", []string{"Critical"}},
		{"後で決めますを検出する", "方式は後で決めます。", []string{"Critical"}},
		{"未定とするを検出する", "期限は未定とする。", []string{"Critical"}},
		{"未定としますを検出する", "期限は未定とします。", []string{"Critical"}},
		{"将来検討を検出する", "拡張は将来検討です。", []string{"Critical"}},
		{"日本語の後の境界スペースを検出する", "これは Goで書きます。", []string{"High"}},
		{"英数字の後の境界スペースを検出する", "Go で書きます。", []string{"High"}},
		{"2つの空白の境界も検出する", "日本語  Goです。", []string{"High"}},
		{"々の後の境界スペースを検出する", "人々 Goです。", []string{"High"}},
		{"見出しの境界スペースは通す", "## 1 Go の版", nil},
		{"箇条書きの見出し語の区切りは通す", "3. 配備先 firstlineと同じホストです。", nil},
		{"プラスの箇条書きの見出し語も通す", "+ 目的 firstlineで書きます。", nil},
		{"括弧の番号の見出し語も通す", "1) 目的 firstlineで書きます。", nil},
		{"箇条書きの本文中の境界スペースは検出する", "- 配備先 これは firstline です。", []string{"High"}},
		{"助詞で終わる1語目は見出し語とみなさない", "- 主催者は Argon2idで認証します。", []string{"High"}},
		{"句読点を含む1語目は見出し語とみなさない", "- これは、 Goで書きます。", []string{"High"}},
	}
	for _, p := range []string{"は", "が", "を", "に", "で", "と", "も", "の", "へ", "や"} {
		tests = append(tests, struct {
			name string
			line string
			want []string
		}{"助詞" + p + "で終わる1語目は見出し語とみなさない", "- 主催者" + p + " Goで書きます。", []string{"High"}})
	}
	for _, c := range []string{"、", "。", "「", "」", "『", "』", "（", "）", "！", "？", "：", "々", "〆", "【", "】", "〈", "〉"} {
		tests = append(tests, struct {
			name string
			line string
			want []string
		}{c + "の後の境界スペースを検出する", "日本" + c + " Goです。", []string{"High"}})
		tests = append(tests, struct {
			name string
			line string
			want []string
		}{c + "の前の境界スペースを検出する", "Go " + c + "です。", []string{"High"}})
	}
	for _, c := range []string{"々", "）", "」", "』", "】"} {
		tests = append(tests, struct {
			name string
			line string
			want []string
		}{c + "の後の文末コロンを検出する", "例" + c + ":", []string{"Medium"}})
	}
	// Range endpoints of every character class, so that narrowing a range by one character is caught.
	jaEdges := []string{"ぁ", "ん", "ァ", "ヿ", "一", "鿿"}
	alnumEdges := []string{"0", "9", "A", "Z", "a", "z"}
	for i, j := range jaEdges {
		a := alnumEdges[i]
		for _, tc := range []struct{ name, line string }{
			{j + "と" + a + "の境界スペースを検出する", j + " " + a + "です。"},
			{a + "と" + j + "の境界スペースを検出する", "x" + a + " " + j + "です。"},
			{j + "の後の文末コロンを検出する", j + ":"},
			{j + "で始まり英字で終わる文末コロンを検出する", j + "b:"},
			{"日本語の後の" + a + "で終わる文末コロンを検出する", "版" + a + ":"},
		} {
			tests = append(tests, struct {
				name string
				line string
				want []string
			}{tc.name, tc.line, []string{severityFor(tc.name)}})
		}
	}
	for _, p := range []string{"、", "。", "，", "．"} {
		tests = append(tests, struct {
			name string
			line string
			want []string
		}{"句読点" + p + "を含む1語目は見出し語とみなさない", "- 主催" + p + "者 Goで書きます。", []string{"High"}})
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := severities(lintLine(tt.line))
			if !slices.Equal(got, append([]string{}, tt.want...)) {
				t.Errorf("lintLine(%q) severities = %v, want %v", tt.line, got, tt.want)
			}
		})
	}
}

func severityFor(name string) string {
	if strings.Contains(name, "境界") {
		return "High"
	}
	return "Medium"
}

func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLintFileFences(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantLines []int
		wantSev   []string
	}{
		{"囲みの中は検査しない", "本文です。\n```\nsqlite3 golang\n```\nmysqlです。\n", []int{5}, []string{"High"}},
		{"チルダの囲みの中も検査しない", "~~~\nsqlite3 **x** TODO\n~~~\nmysqlです。\n", []int{4}, []string{"High"}},
		{"長い囲みの中の短い記号では閉じない", "````md\n```\nmysql\n```\n````\nmysqlです。\n", []int{6}, []string{"High"}},
		{"違う記号では閉じない", "```\n~~~\nmysql\n```\nmysqlです。\n", []int{5}, []string{"High"}},
		{"閉じない囲みを検出する", "本文です。\n```\nmysql\n", []int{2}, []string{"High"}},
		{"記号の後に空白だけがある行で閉じる", "```\nmysql\n```  \nmysqlです。\n", []int{4}, []string{"High"}},
		{"チルダの開きは後ろにバッククォートがあっても囲みとする", "~~~a`b\nmysql\n~~~\nmysqlです。\n", []int{4}, []string{"High"}},
		{"記号の後に文字がある行では閉じない", "```\n```go\nmysql\n```\nmysqlです。\n", []int{5}, []string{"High"}},
		{"箇条書きの中の字下げした囲みも認める", "- 手順です。\n\n    ```sh\n    mysql TODO\n    ```\nmysqlです。\n", []int{6}, []string{"High"}},
		{"バッククォートを含む開きの行は囲みとみなさない", "```a```を使います。TODOです。\nmysqlです。\n", []int{1, 2}, []string{"Critical", "High"}},
		{"開きより長い記号でも閉じる", "```\nx\n````\nmysqlです。\n", []int{4}, []string{"High"}},
		{"囲みの開きの行は検査しない", "```mysql\nx\n```\n", nil, nil},
		{"BOMで始まる見出しは見出しとして扱う", string([]byte{0xEF, 0xBB, 0xBF}) + "## 1 Go の版\n", nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := lintFile(writeFile(t, t.TempDir(), "a.md", tt.body))
			var lines []int
			for _, f := range got {
				lines = append(lines, f.line)
			}
			if !slices.Equal(lines, tt.wantLines) || !slices.Equal(severities(got), tt.wantSev) {
				t.Errorf("lintFile = %v, want lines %v severities %v", got, tt.wantLines, tt.wantSev)
			}
		})
	}
}

func TestLintFileReportsReadError(t *testing.T) {
	got := lintFile(filepath.Join(t.TempDir(), "missing.md"))
	if !slices.Equal(severities(got), []string{"Critical"}) {
		t.Errorf("lintFile(missing) = %v, want 1 Critical", got)
	}
}

func TestRun(t *testing.T) {
	tests := []struct {
		name     string
		files    map[string]string
		wantExit int
		wantOut  string
	}{
		{"違反がなければ0", map[string]string{"a.md": "SQLiteです。\n"}, 0, "Critical 0 / High 0 / Medium 0 / Low 0"},
		{"Criticalがあれば1", map[string]string{"a.md": "TBDです。\n"}, 1, "Critical 1 / High 0 / Medium 0 / Low 0"},
		{"所見の行にパスと行番号を出す", map[string]string{"a.md": "本文です。\nTBDです。\n"}, 1, "a.md:2 do not leave deferral markers"},
		{"Highがあれば1", map[string]string{"a.md": "mysqlです。\n"}, 1, "Critical 0 / High 1 / Medium 0 / Low 0"},
		{"Mediumがあれば1", map[string]string{"a.md": "次のとおり:\n"}, 1, "Critical 0 / High 0 / Medium 1 / Low 0"},
		{"md以外は検査しない", map[string]string{"a.txt": "TBDです。\n"}, 0, "Critical 0 / High 0 / Medium 0 / Low 0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, body := range tt.files {
				writeFile(t, dir, name, body)
			}
			var out strings.Builder
			if got := run(dir, &out); got != tt.wantExit {
				t.Errorf("run exit = %d, want %d", got, tt.wantExit)
			}
			if !strings.Contains(out.String(), tt.wantOut) {
				t.Errorf("run output = %q, want it to contain %q", out.String(), tt.wantOut)
			}
		})
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, os.ErrClosed }

func TestRunReportsWriteError(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.md", "SQLiteです。\n")
	if got := run(dir, failWriter{}); got != 2 {
		t.Errorf("run(failing writer) exit = %d, want 2", got)
	}
}

func TestRootArg(t *testing.T) {
	if got := rootArg([]string{"doclint"}); got != "docs" {
		t.Errorf("rootArg(no args) = %q, want docs", got)
	}
	if got := rootArg([]string{"doclint", "../../docs"}); got != "../../docs" {
		t.Errorf("rootArg(one arg) = %q, want ../../docs", got)
	}
}

func TestRunReportsMissingRoot(t *testing.T) {
	var out strings.Builder
	if got := run(filepath.Join(t.TempDir(), "missing"), &out); got != 2 {
		t.Errorf("run(missing) exit = %d, want 2", got)
	}
}
