# 設計文書の作成計画

`docs/requirements/scope-and-decisions.md`の決定事項から、doclint、基本設計書、ADRを作ります。設計文書の作業は`design-document-quality-gate`スキルの手順に従います。

## 手順

1. doclintを導入する
   - `tools/doclint/`に独立したGoモジュールとして置く。アプリのカバレッジとミューテーションの対象から外すため
   - awase-go-htmxの`tools/doclint/main.go`を元にして、強調、文末コロン、先送りの語、日本語と英数字の間の半角スペース、用語統一を検証する
   - 用語統一にはSQLite、Go、Cloudflare、Litestream、PostgreSQL、MySQLを入れる
   - `docs/_quality/`に`QUALITY_RUBRIC.md`、`STYLE_GUIDE.md`、`SELF_REVIEW_LOG.md`を置く。STYLE_GUIDEの規則はdoclintの規則表と同時に変える
   - `go test ./... && go run . ../../docs`が通ることを確かめる
2. 決定表を固める。`scope-and-decisions.md`の「設計書で決めること」を、選択肢、採用、理由、ADRの番号の表にして、未決の行を残さない
3. 基本設計書を`docs/design/basic-design.md`に書く。決定と設定値の対応表、ReadDBとWriteDBの接続設定、グループコミット、在庫、決済、状態遷移、認証、画面、文字列の言語、配備（systemd、Litestream、復元スクリプト）を含める
4. ADR-0001からADR-0005を`docs/design/adr/`に書く。中心はADR-0003（規模拡大の判断基準と移行先）にする
5. 節を書くたびにdoclintを通し、自己レビューの10項目を`SELF_REVIEW_LOG.md`に記録する
6. doclintが0件になってから、敵対的レビューを3人で行う（2人以上の一致で採用、最大3回。上限に届いたら止めて、続けるかを利用者が決める）
