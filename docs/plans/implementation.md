# firstlineの実装計画

> 実行者へ。この計画はタスクごとに、`superpowers:subagent-driven-development`か`superpowers:executing-plans`で進めます。手順はチェックボックス（`- [ ]`）で進み具合を記録します。

| 項目 | 内容 |
|---|---|
| 目的 | 基本設計書のとおりに動く`firstline`の単一バイナリと、その配備の設定を作る |
| 構成 | 書き込みは1つのゴルーチンがグループコミットで行い、読み取りは別の接続で行う。業務ロジックは`internal/app`に置き、保存先、在庫、決済、メール、観測は`internal/port`のinterfaceで差し替える |
| 技術 | Go 1.27、`modernc.org/sqlite`、Bun、`html/template`とhtmx、TLA+（TLC） |
| 仕様 | `docs/design/basic-design.md`、`docs/design/adr/ADR-0001`〜`ADR-0005`、`docs/requirements/scope-and-decisions.md` |

実行者は計画と仕様の両方を読みます。計画に値がない所では仕様の値に従います。

## 全体の制約

次の制約はすべてのタスクに当てはまります。

- モジュール名は`github.com/okamyuji/firstline`、`go.mod`の`go`の行は`1.27`にする
- 依存は次の版を使う。どれも公開から3日以上たっている
  - `github.com/uptrace/bun`とその`dialect/sqlitedialect`、`dialect/pgdialect`、`dialect/mysqldialect`、`driver/pgdriver`は`v1.3.0`
  - `modernc.org/sqlite`は`v1.63.0`、`github.com/go-sql-driver/mysql`は`v1.10.2`
  - `golang.org/x/text`は`v0.44.0`、`golang.org/x/crypto`は`v0.59.0`
  - `github.com/testcontainers/testcontainers-go`と`modules/postgres`、`modules/mysql`は`v0.45.0`
  - htmxは`2.0.8`、TLA+の`tla2tools.jar`は`v1.7.4`
- Claude Codeのサンドボックスで`go get`を実行するときは`SSL_CERT_FILE=/etc/ssl/cert.pem`を付ける。付けないとプロキシの証明書の検証で失敗する
- Goのファイルを編集する前に、`modern-go-guidelines:use-modern-go`のスキルで指針を確かめ、当てはまる指針に従う
- SQLはGoの文字列に書かない。`internal/store/<方言>/sql/<名前>.sql`に置き、`embed.FS`で読み、`db.NewRaw`で実行する。値は必ず`?`で渡す
- 時刻の列はUTCのUnixミリ秒の`BIGINT`にする。IDは`crypto/rand`の16バイトをbase64urlにした文字列で、`mail_outbox.id`だけをDBが採番する
- 利用者が読む文字列は`internal/i18n`の日本語と英語の`map`から取る。ログ、サーバー内部のエラー文、CLIの出力は英語だけで書く
- 時刻は`app.Clock`（`func() time.Time`）から取り、`time.Now`を直接呼ばない。試験で期限の境界を作るため
- 品質ゲートは`scripts/check.sh`がすべて通ること。変更行の変異はすべて殺し、カバレッジは80%以上、関数ごとのCRAPは15以下にする
- コミットはタスクごとに1つ以上、conventional commitsの形で書く。コミットの前に`ponytail:ponytail-review`を通す

## レビューで見る入力

仕様が触れていないが、使う人が出会いやすい入力です。それぞれの試験を、行の末尾に書いたタスクに入れています。

1. 販売開始の直後に、定員を超える数の保留が同時に来る。定員を超えて売らない（タスク10）
2. 決済の完了時刻が`expires_at`とちょうど等しい。払い戻しへ進む。期限切れの処理は`expires_at`のちょうど1分後に動く（タスク3、タスク11）
3. 同じ完了通知が2回届く、失敗の通知の後に完了の通知が届く。席を2回戻さず、払い戻しを2回しない（タスク11）
4. 保留が残ったまま再起動する。在庫は保留の枚数を引いた数になり、照合は期限切れより先に動く（タスク8、タスク19）
5. 空、`*`、`en;q=0`、壊れた値の`Accept-Language`を受けても、日本語の画面を返して500にしない（タスク12）

## ファイルの構成

| 置き場 | 役割 | 作るタスク |
|---|---|---|
| `spec/Purchase.tla`、`spec/Purchase.cfg`、`scripts/tlc.sh` | 状態遷移のTLA+の仕様と検査 | 2 |
| `internal/domain` | 状態、遷移の表、エラーの型 | 3 |
| `internal/port` | 差し替え口のinterfaceと共通の型 | 4 |
| `internal/store/writer` | グループコミットの書き込みゴルーチン | 5 |
| `internal/store` | 方言の共通部分（SQLファイルの実行、表の作成） | 6 |
| `internal/store/sqlite`、`postgres`、`mysql` | 方言ごとの接続とSQLファイル | 6、7、18 |
| `internal/store/storetest` | 3方言に同じ試験を通す共通の試験 | 7 |
| `internal/inventory` | メモリ実装とDB実装 | 8 |
| `internal/payment/fake`、`internal/mail/fake` | 模擬実装 | 9 |
| `internal/app` | 購入、照合、期限切れ、払い戻し、メール送信、主催者の操作 | 10、11、13、14 |
| `internal/i18n` | 文字列と言語の判定 | 12 |
| `internal/auth` | 照会トークン、Argon2id、試行制限 | 12、14 |
| `internal/web` | 経路、テンプレート、静的ファイル | 15、16 |
| `internal/observe` | `logreporter`、`emf`、`/healthz`の集計 | 17 |
| `internal/migrate` | `migrate-data`の本体 | 18 |
| `cmd/firstline` | 設定、組み立て、起動と停止、サブコマンド | 19 |
| `deploy/`、`scripts/restore.sh` | systemd、Litestream、CloudWatch Agent、logrotate、certbotの設定 | 20 |
| `e2e/` | 実際のバイナリを動かす試験 | 21 |

## タスク

### タスク1 モジュールの雛形と品質ゲート

- 作る `go.mod`、`.gitignore`、`.golangci.yml`、`scripts/check.sh`、`scripts/crap.sh`、`.github/workflows/ci.yml`

- [ ] `go mod init github.com/okamyuji/firstline`を実行し、`go`の行を`1.27`にする
- [ ] `.gitignore`に`/bin/`、`/tools/tla/`、`*.out`、`/e2e/tmp/`を書く
- [ ] `scripts/check.sh`を書く。`set -euo pipefail`の後に次を順に実行し、1つでも失敗したら止まる
  - `gofmt -l .`の出力が空であること
  - `go vet ./...`、`staticcheck ./...`、`golangci-lint run`、`go build ./...`
  - `go test -count=1 -coverprofile=cover.out ./...`と、合計のカバレッジが80%以上であること
  - `gremlins unleash --diff "${BASE:-HEAD~1}"`の結果に生き残った変異がないこと
  - `scripts/crap.sh cover.out`が終了コード0であること
  - `(cd tools/doclint && go test -count=1 ./... && go run . ../../docs)`
  - `scripts/tlc.sh`（タスク2で作る。ないうちは飛ばす）
- [ ] `scripts/crap.sh`を書く。`gocyclo -over 0 .`の複雑度`c`と`go tool cover -func`の網羅率`p`を関数ごとに突き合わせ、`c*c*(1-p)^3 + c`が15を超える関数を表示して終了コード1で終わる
- [ ] `scripts/crap.sh`の確認として、複雑度16で試験のない関数を置いた一時のパッケージで実行し、その関数を表示して失敗することを確かめる
- [ ] `new-project-ci`のスキルで、`okamyuji/reusable-workflows@v1`を呼ぶ`.github/workflows/ci.yml`を作る。gitleaksとGoのジョブを入れる
- [ ] `bash scripts/check.sh`を実行し、終了コード0を確かめる
- [ ] コミットする（`chore: scaffold module and quality gate`）

### タスク2 状態遷移のTLA+仕様

- 作る `spec/Purchase.tla`、`spec/Purchase.cfg`、`scripts/tlc.sh`
- 出力 TLCで確かめた遷移の表で、タスク3はこれと同じ遷移を実装する

- [ ] `scripts/tlc.sh`を書く。`tools/tla/tla2tools.jar`がなければ`https://github.com/tlaplus/tlaplus/releases/download/v1.7.4/tla2tools.jar`から取得し、SHA-256を照合する。照合する値は最初の取得で測ってスクリプトに固定し、以後は一致しなければ止める。続けて`java -cp tools/tla/tla2tools.jar tlc2.TLC -config spec/Purchase.cfg spec/Purchase.tla`を実行する
- [ ] `spec/Purchase.tla`を書く。モデルの要素は次のとおり
  - 定数 `Capacity`、`Buyers`（購入者の集合）
  - 購入ごとの変数 状態、`paid`（決済が完了したか）、`notified`（完了の通知が処理済みか）、`refundDone`
  - 動作 保留の作成、`Start`の失敗、失敗の通知、期限内の完了通知、期限後の完了通知、期限切れ、払い戻しの成功、記録のない購入への完了通知、照合（完了済みの決済を通知と同じ処理に流す）
  - 仕様の遷移の表にない組み合わせの動作は書かない
- [ ] 不変条件を書く。`NoOversell`は`held`と`purchased`の枚数の合計が`Capacity`以下、`ReleaseOnce`は席を戻した回数が購入ごとに1以下であること
- [ ] 活性を書く。`PaidEndsWell`は`paid`の購入がいずれ`purchased`か`refunded`になること。期限切れ、払い戻しの再試行、照合の動作に`WF_vars`を付ける
- [ ] `spec/Purchase.cfg`を`Capacity = 2`、`Buyers = {b1, b2, b3}`で書き、`INVARIANT NoOversell ReleaseOnce`と`PROPERTY PaidEndsWell`を入れる
- [ ] `scripts/tlc.sh`を実行し、`Model checking completed. No error has been found.`が出ることを確かめる
- [ ] 不変条件が働くことを確かめる。保留の作成から`remaining`の条件を外した写しで実行すると`NoOversell`が破れることを見て、写しを捨てる
- [ ] コミットする（`feat(spec): add TLA+ model of purchase states`）

### タスク3 購入の状態と遷移の表

- 作る `internal/domain/purchase.go`、`internal/domain/errors.go`
- 試験 `internal/domain/purchase_test.go`
- 出力

```go
type State string // "held" "purchased" "released" "expired" "refunding" "refunded"
type Event int    // EvStartFailed, EvPaymentFailed, EvPaymentCompleted, EvExpire, EvRefundSucceeded
type Facts struct {
	CompletedAt time.Time // EvPaymentCompletedのときだけ使う
	ExpiresAt   time.Time
	Now         time.Time
}
type MailKind string // "" "hold" "purchased" "refund"
type Decision struct {
	To          State
	Changed     bool     // falseなら状態を変えずに成功を返す
	ReleaseSeat bool     // event_salesのheldを減らしremainingを増やし、コミット後にInventory.Releaseを呼ぶ
	SellSeat    bool     // heldを減らしsoldを増やし、整理番号を付ける
	Charge      bool     // billing_logsにchargeを書く
	Refunded    bool     // billing_logsにrefundを書き、event_salesのrefundsを増やす
	Mail        MailKind
}
func Next(from State, ev Event, f Facts) (Decision, error)

const HoldTTL = 10 * time.Minute
const ExpiryGrace = time.Minute
var ErrSoldOut, ErrInvalidTransition, ErrNotOnSale, ErrBadQuantity, ErrDuplicateHold, ErrNotFound error
```

- [ ] 遷移の表の行ごとに1つの試験を、表で駆動する形で書く。`TestNext`の行は仕様の状態遷移の表と同じ順に並べ、各行で`Decision`の全項目を比べる。境界の行を入れる
  - `held`に`EvPaymentCompleted`で`CompletedAt`が`ExpiresAt`の1ナノ秒前なら`purchased`、`SellSeat`、`Charge`、`Mail == "purchased"`
  - `CompletedAt == ExpiresAt`なら`refunding`、`ReleaseSeat`、`Mail == "refund"`、`Charge`は`false`
  - `held`に`EvExpire`で`Now == ExpiresAt.Add(ExpiryGrace)`なら`expired`、1ナノ秒前なら`ErrInvalidTransition`
  - `purchased`、`refunding`、`refunded`への完了通知は`Changed == false`でエラーなし
  - `held`以外への失敗通知は`Changed == false`でエラーなし
  - `expired`と`released`への完了通知は`refunding`で、`ReleaseSeat`は`false`
- [ ] 表にない組み合わせを網羅する試験`TestNextRejectsUnlistedPairs`を書く。6状態と5契機の30組から表にある組を除いたすべてで`errors.Is(err, ErrInvalidTransition)`になること
- [ ] `go test ./internal/domain`を実行し、`undefined: Next`で失敗することを確かめる
- [ ] `Next`を実装する。`switch`で表の行を書き、表と同じ順に並べる
- [ ] `go test -count=1 ./internal/domain`が通ることを確かめ、`gremlins unleash ./internal/domain`で変異がすべて殺されることを確かめる
- [ ] コミットする（`feat(domain): add purchase state transitions`）

### タスク4 差し替え口

- 作る `internal/port/port.go`、`internal/app/clock.go`
- 出力 仕様の在庫、決済、観測の節に書いた`Inventory`、`Payment`、`Completion`、`PaymentNotifier`、`ErrorReporter`、`MetricsSink`をそのまま置く。加えて次を置く

```go
type Mailer interface {
	Send(ctx context.Context, m Mail) error
}
type Mail struct{ To, Lang, Subject, Body string }
type HealthSnapshot struct {
	CommitWaitP99Ms       float64
	SyncItemsPerSecMax5m  int
	QueueLen, QueueCap    int
	QueueUsageAvg5m       float64
	DroppedTotal          int64
	WALBytes, DBBytes     int64
	LastReconcileAt       time.Time
	TLSCertDaysLeft       int
}
// internal/app
type Clock func() time.Time
```

- [ ] `go build ./...`が通ることを確かめる（interfaceだけなので試験はない）
- [ ] コミットする（`feat(port): add replaceable interfaces`）

### タスク5 グループコミットの書き込みゴルーチン

- 作る `internal/store/writer/writer.go`
- 試験 `internal/store/writer/writer_test.go`（SQLiteの一時ファイルを`bun.DB`で開いて使う）
- 入力 `*bun.DB`
- 出力

```go
type Apply func(ctx context.Context, tx bun.Tx) error
type Options struct {
	QueueCap  int           // 8192
	BatchMax  int           // 64
	BatchWait time.Duration // 15ms
	IsIOError func(error) bool
	OnFatal   func(error)   // 本番ではos.Exit(1)
	Now       func() time.Time
	After     func(time.Duration) <-chan time.Time
	OnSyncResult func(at time.Time, wait time.Duration) // waitはDoを呼んでから結果まで
	OnAsyncError func(error)                            // 本番ではErrorReporter.Report
}
func New(db *bun.DB, opt Options) (*Writer, error)     // 関数のnilと0以下の値をエラーにする
func (w *Writer) Run()                                 // Stopまでコミットを続ける
func (w *Writer) Do(ctx context.Context, a Apply) error // 同期項目
func (w *Writer) Send(a Apply)                         // 非同期項目。満杯なら捨てて数える
func (w *Writer) Stop()                                // 残りをコミットしてRunを終える
func (w *Writer) Stats() Stats                         // HealthSnapshotの書き込みの項目を作る値
var ErrShuttingDown error
```

- [ ] 次の試験を書く
  - `TestBatchesUpTo64`は非同期項目を200件送り、コミットの回数が`ceil(200/64)`以上で全件が表に入ること
  - `TestFlushesAfter15ms`は非同期項目を1件だけ送り、偽の時計を15ms進めると表に入ること
  - `TestSyncItemFlushesWithoutWaiting`は同期項目1件の`Do`が、15msを待たずに戻ること（偽の時計を進めない）
  - `TestSyncItemsShareOneCommit`はチャネルに同期項目を50件ためてから`Run`を始め、コミットが1回で全件の`Do`が`nil`を返すこと
  - `TestFailedItemGetsItsOwnError`は64件のうち1件だけが一意の制約に反し、その1件だけがエラーで残りの63件が表に入ること
  - `TestIOErrorCallsOnFatalAndRepliesNothing`は`IsIOError`が真を返すとき、`OnFatal`が1回呼ばれ、`Do`がどの値も返さないこと
  - `TestDoWaitsAfterEnqueueEvenIfCtxDone`は項目を送った後で`ctx`を取り消しても、`Do`がコミットの結果を返すこと
  - `TestDoReturnsCtxErrWhenQueueFull`は満杯のチャネルで、`Do`が`ctx`の期限で`context.DeadlineExceeded`を返し、項目が送られないこと
  - `TestSendDropsWhenFull`は満杯で`Send`が捨て、`Stats().DroppedTotal`が増えること
  - `TestStopDrainsAndRejects`は`Stop`の前に送った項目がすべてコミットされ、`Stop`の後の`Do`が`ErrShuttingDown`を返すこと。チャネルは閉じないので`Send`もpanicしないこと
- [ ] `go test ./internal/store/writer`を実行し、未定義で失敗することを確かめる
- [ ] 実装する。仕様のグループコミットの規則1〜4と、項目ごとのセーブポイントで失敗した項目だけを巻き戻す処理を書く
- [ ] `go test -race -count=1 ./internal/store/writer`が通ることと、変異がすべて殺されることを確かめる
- [ ] コミットする（`feat(store): add group-commit writer`）

### タスク6 SQLiteの接続と表

- 作る `internal/store/store.go`、`internal/store/sqlite/sqlite.go`、`internal/store/sqlite/sql/schema.sql`
- 試験 `internal/store/sqlite/sqlite_test.go`
- 出力

```go
// internal/store
type Store struct {
	Write, Read *bun.DB
	SQL         fs.FS
	IsIOError   func(error) bool
	IsUnique    func(error) bool
}
func (s *Store) Exec(ctx context.Context, db bun.IDB, name string, args ...any) (sql.Result, error)
func (s *Store) Select(ctx context.Context, db bun.IDB, dest any, name string, args ...any) error
func (s *Store) Migrate(ctx context.Context) error // schema.sqlを文ごとに実行する
// internal/store/sqlite
func Open(ctx context.Context, path string) (*store.Store, error)
func WriteDSN(path string) string
func ReadDSN(path string) string
```

- [ ] 次の試験を書く
  - `TestDSNMatchesDesign`は`WriteDSN("/var/lib/firstline/firstline.db")`と`ReadDSN(...)`が、仕様の接続設定の節の文字列と完全に一致すること
  - `TestReadDBRejectsWrites`は`Read`への`INSERT`が`SQLITE_READONLY`で失敗すること
  - `TestReadDBDoesNotCreateFile`は存在しないパスで`ReadDSN`を開くと失敗し、ファイルができないこと
  - `TestPragmasApplied`は`Write`で`journal_mode`が`wal`、`synchronous`が`2`、`wal_autocheckpoint`が`0`、`cache_size`が`-16384`。`Read`で`mmap_size`が`268435456`、`cache_size`が`-65536`であること
  - `TestOpenOrder`は新しいファイルで`Open`が成功すること（WriteDBをWALにしてからReadDBを開く順の確認）
  - `TestStrictPragmas`は`_pragma`に2つの文を入れた接続文字列で開くと失敗すること
  - `TestCommittedRowVisibleToRead`は`Write`でコミットした行が次の`Read`の読み取りで見えること
- [ ] 失敗を確かめ、実装する。`Open`は`sqlite.StrictPragmas(true)`を呼び、`SetMaxOpenConns`と`SetMaxIdleConns`を仕様の表の値にする。`IsIOError`は`SQLITE_IOERR`の系統を真にする
- [ ] `schema.sql`に仕様の表の節の9つの表を書く。`organizers.email`と`orphan_refunds.payment_id`に一意の制約、`mail_outbox.id`は`INTEGER PRIMARY KEY`
- [ ] 試験が通ることを確かめ、コミットする（`feat(store): open SQLite with split read/write pools`）

### タスク7 SQLファイルと3方言の共通試験

- 作る `internal/store/sqlite/sql/*.sql`、`internal/store/storetest/storetest.go`
- 試験 `internal/store/sqlite/storetest_test.go`
- 入力 タスク6の`store.Store`
- 出力 次の表の名前のSQLファイルを、3方言とも同じ名前と同じ引数の順で作る

| 名前 | 働き |
|---|---|
| `insert_organizer` | 主催者を作る（小文字のメールアドレス） |
| `get_organizer_by_email` | ログインの照合 |
| `insert_event`、`insert_event_sales` | イベントと`event_sales`の行を作る（`remaining`は定員） |
| `get_event`、`list_public_events`、`list_events_by_organizer`、`get_sales` | 読み取り |
| `insert_purchase` | `held`の購入を作る |
| `hold_seats` | `remaining >= ?`を条件に`held`を増やし`remaining`を減らす |
| `count_active_holds` | 同じメールアドレスと同じイベントの`held`を数える |
| `record_payment` | `payment_id`が空のときだけ`payment_id`と`checkout_url`を書く |
| `get_purchase` | 購入を1件読む |
| `set_state` | `WHERE id = ? AND state = ?`で状態を変える |
| `release_seats`、`sell_seats`、`count_refund` | `event_sales`の数を動かす |
| `assign_ticket` | `events.next_ticket_number`を進め、購入に番号を付ける |
| `insert_billing` | 課金ログを書く |
| `insert_orphan`、`list_refunding_orphans`、`set_orphan_refunded` | 記録のない決済の払い戻し |
| `list_due_holds`、`list_refunding` | 期限切れと払い戻しの対象を探す |
| `insert_mail`、`list_unsent_mail`、`mark_mail_sent` | 送信待ちのメール |
| `inventory_counts` | イベントごとの定員と、`held`と`purchased`の枚数の合計 |
| `insert_session`、`get_session`、`touch_session`、`delete_session`、`delete_expired_sessions` | 主催者のセッション |
| `insert_access_log` | アクセスログ |

```go
// internal/store/storetest
type Factory func(t *testing.T) *store.Store
func Run(t *testing.T, f Factory)
```

- [ ] `storetest.Run`に試験を書く。表の名前ごとに1つ以上の試験を置き、次を必ず入れる
  - `hold_seats`は`remaining`が枚数ちょうどなら成功し、1枚足りなければ更新件数0になること
  - `set_state`は元の状態が違うと更新件数0になること
  - `record_payment`は2回目に違う値を渡しても上書きしないこと
  - `insert_orphan`は同じ`payment_id`の2回目でエラーにならず、行が1つのままであること
  - `list_unsent_mail`は`id`の昇順で返すこと
  - `insert_organizer`は大文字違いの同じアドレスを一意の制約で拒むこと（保存の前に小文字にするのは呼び出し側。ここでは小文字どうしの重複を確かめる）
  - 方言のSQLファイルの名前の集合が、上の表と一致すること
- [ ] SQLiteで`storetest.Run`を呼ぶ試験を書き、失敗を確かめる
- [ ] SQLファイルを書き、試験が通ることを確かめる
- [ ] コミットする（`feat(store): add named SQL files and shared store tests`）

### タスク8 在庫

- 作る `internal/inventory/memory.go`、`internal/inventory/db.go`、`internal/app/rebuild.go`
- 試験 `internal/inventory/memory_test.go`、`internal/app/rebuild_test.go`
- 出力 `inventory.NewMemory() port.Inventory`、`inventory.NewDB() port.Inventory`、`func (s *Service) RebuildInventory(ctx context.Context) error`

- [ ] 次の試験を書く
  - メモリ実装は`Add`の後の`Reserve`が残数を減らし、残数を超えると`domain.ErrSoldOut`、`Release`で戻ること
  - メモリ実装に200のゴルーチンから`Reserve(1)`を同時に送り、定員10なら成功がちょうど10件であること（`-race`で実行）
  - DB実装の`Reserve`と`Release`は何もせず`nil`を返すこと
  - `RebuildInventory`は`held`2枚、`purchased`3枚、`expired`4枚の定員10のイベントで、残数5を`Add`すること
- [ ] 失敗を確かめ、実装する
- [ ] 試験が通ることを確かめ、コミットする（`feat(inventory): add memory and db inventories`）

### タスク9 決済とメールの模擬実装

- 作る `internal/payment/fake/fake.go`、`internal/mail/fake/fake.go`
- 試験 それぞれの`_test.go`
- 出力

```go
// internal/payment/fake
type Outcome int // Complete, Fail
type Fake struct{ /* ... */ }
func New(clock func() time.Time, delay time.Duration, outcome Outcome) *Fake
func (f *Fake) SetNotifier(n port.PaymentNotifier) // Serviceを作った後に渡す。渡す前のStartは通知を保留する
func (f *Fake) Stop()                 // 通知の送信をやめる
func (f *Fake) FailStart(err error)   // 次のStartをエラーにする（試験用）
// internal/mail/fake
type Fake struct{ /* ... */ }
func (f *Fake) Sent() []port.Mail
func (f *Fake) FailNext(n int)        // 次のn回のSendをエラーにする（試験用）
```

- [ ] 次の試験を書く
  - `Start`は空でない`paymentID`と空の`checkoutURL`を返すこと
  - 遅延の後に`Completed`か`Failed`が呼ばれ、`Completion.At`が完了させた時刻であること
  - 通知先がエラーを返すと5秒ごとに送り直し、成功で止まること
  - `Completed(since)`は`since`以後の完了だけを返すこと
  - `Refund`は同じ`payment_id`に2回呼んでも成功し、払い戻しの回数が1であること
  - `Stop`の後は通知を送らないこと
  - `SetNotifier`の前に`Start`した決済の通知は、`SetNotifier`の後に送られること
- [ ] 失敗を確かめ、実装し、試験が通ることを確かめる
- [ ] コミットする（`feat: add fake payment and mailer`）

### タスク10 保留の作成と決済の開始

- 作る `internal/app/service.go`、`internal/app/hold.go`
- 試験 `internal/app/hold_test.go`（SQLiteの一時ファイル、本物の`writer`、模擬の決済）
- 入力 タスク3〜9の型
- 出力

```go
type Deps struct {
	Store     *store.Store
	Writer    *writer.Writer
	Inventory port.Inventory
	Payment   port.Payment
	Errors    port.ErrorReporter
	Clock     Clock
	NewID     func() string
}
func New(d Deps) *Service
type HoldInput struct {
	EventID, Email, Lang string
	Quantity             int
}
type HoldResult struct {
	PurchaseID, CheckoutURL string
	ExpiresAt               time.Time
}
func (s *Service) Hold(ctx context.Context, in HoldInput) (HoldResult, error) // 保留、Start、payment_idの記録まで
```

- [ ] 次の試験を書く
  - 成功すると`held`の購入、`event_sales`の`held`と`remaining`の変化、`kind = "hold"`の`mail_outbox`の行、`amount = price * quantity`ができること
  - `ExpiresAt`が`Clock()`の10分後であること
  - 枚数0と5は`ErrBadQuantity`、販売開始の1ミリ秒前とイベント開始の時刻ちょうどは`ErrNotOnSale`
  - 同じメールアドレスで同じイベントの`held`が残っていると`ErrDuplicateHold`
  - 定員10に100のゴルーチンから1枚ずつ`Hold`を呼び、成功がちょうど10件で、`held`の合計が10、`remaining`が0であること。メモリ在庫とDB在庫の両方で確かめる
  - 書き込みがエラーなら`Inventory.Release`で席が戻ること
  - `Start`がエラーなら購入が`released`になり、席が戻ること
  - `record_payment`がエラーでも購入は`held`のまま`Hold`が成功を返すこと
- [ ] 失敗を確かめ、実装する。順は仕様の在庫の節と決済の節の購入の流れのとおり
- [ ] 試験が通ることを確かめ、コミットする（`feat(app): create holds and start payment`）

### タスク11 決済の通知と状態の遷移

- 作る `internal/app/notify.go`、`internal/app/transition.go`
- 試験 `internal/app/notify_test.go`
- 出力 `*Service`が`port.PaymentNotifier`を満たす。`func (s *Service) apply(ctx context.Context, purchaseID string, ev domain.Event, f domain.Facts) error`は遷移を1回の同期項目で書く共通の処理

- [ ] 次の試験を書く
  - 期限内の完了で`purchased`、整理番号が1から振られ、`charge`の課金ログと`kind = "purchased"`のメールができること
  - `Completion.At == expires_at`の完了で`refunding`、席が戻り、`charge`がなく、`kind = "refund"`のメールができること
  - 同じ完了通知を2回送ると、2回目は何も変えずに`nil`を返し、整理番号も課金ログも増えないこと
  - 失敗の通知の後に完了の通知が来ると、`released`から`refunding`へ進み、席を2回戻さないこと（`event_sales.remaining`が定員を超えない）
  - `payment_id`が空の`held`に完了が届くと、通知の値を書くこと
  - 購入の記録がない完了通知で`orphan_refunds`に1行でき、2回目でも1行のままであること
  - `Completion.Amount`が`purchases.amount`と違うと`ErrorReporter.Report`が1回呼ばれ、遷移は進むこと
  - 完了通知と期限切れを同じ購入へ並行に流しても、最終の状態が遷移の表のどれか1つに定まり、席を戻す回数が1以下であること（`-race`で100回くり返す）
- [ ] 失敗を確かめ、実装する。更新件数0のときは購入を読み直して表を引き直す
- [ ] 試験が通ることを確かめ、コミットする（`feat(app): apply payment notifications`）

### タスク12 文字列、言語、照会トークン

- 作る `internal/i18n/i18n.go`、`internal/i18n/ja.go`、`internal/i18n/en.go`、`internal/auth/token.go`
- 試験 `internal/i18n/i18n_test.go`、`internal/auth/token_test.go`
- 出力

```go
type Translator interface{ T(lang, key string, args ...any) string }
func New() Translator
func Negotiate(acceptLanguage string) string // "ja"か"en"
func ErrorKey(err error) string             // ドメインのエラーを文のキーへ変える
// internal/auth
type Tokens struct{ Key []byte }
func (t Tokens) Issue(purchaseID string, exp time.Time) string
func (t Tokens) Verify(tok string, now time.Time) (purchaseID string, err error)
```

- [ ] 次の試験を書く
  - 日本語と英語のキーの集合が一致し、空の文がないこと
  - `Negotiate`は`""`、`"*"`、`"en;q=0"`、`"%%%"`、`"fr"`で`"ja"`、`"en-US"`と`"en-GB,ja;q=0.5"`で`"en"`、`"ja,en"`で`"ja"`を返すこと
  - `ErrorKey`は`domain`のエラーごとに専用のキーを返し、それ以外のエラーで汎用のキーを返すこと
  - `Tokens.Verify`は署名を1バイト変えると失敗し、期限の1ミリ秒後に失敗すること。比較は`hmac.Equal`で行うこと
- [ ] 失敗を確かめ、`golang.org/x/text/language`の`Matcher`で実装し、試験が通ることを確かめる
- [ ] コミットする（`feat(i18n): add ja/en strings, negotiation and purchase tokens`）

### タスク13 定期の処理

- 作る `internal/app/expire.go`、`internal/app/refund.go`、`internal/app/reconcile.go`、`internal/app/mailer.go`、`internal/app/loop.go`
- 試験 それぞれの`_test.go`
- 出力

```go
func (s *Service) ExpireDue(ctx context.Context) (int, error)
func (s *Service) RetryRefunds(ctx context.Context) (int, error)
func (s *Service) Reconcile(ctx context.Context, since time.Time) (int, error)
func (s *Service) SendMail(ctx context.Context, m port.Mailer, tr i18n.Translator, tok auth.Tokens, baseURL string) (int, error)
func (s *Service) LastReconcileAt() time.Time
func Every(ctx context.Context, d time.Duration, f func(context.Context)) // ctxの取り消しで止まる
```

- [ ] 次の試験を書く
  - `ExpireDue`は`expires_at`の1分後ちょうどの`held`を`expired`にし、1ミリ秒前のものは残すこと
  - `RetryRefunds`は`refunding`の購入と`orphan_refunds`の`refunding`の行を`refunded`にし、`refund`の課金ログを`purchases.amount`と`orphan_refunds.amount`で書くこと。`Refund`がエラーなら状態を残すこと
  - `Reconcile`は`since`以後の完了を完了通知と同じ処理に流し、`LastReconcileAt`を成功の時刻にすること
  - `SendMail`は保留の通知を`held`でなければ送らず`sent_at`だけを書くこと
  - `SendMail`は`payment_id`が空の保留の通知を次の回へ回し、保留の作成から1分を過ぎたらリンクなしで送ること
  - `SendMail`は失敗した行を飛ばして次へ進み、同じ行が10回続けて失敗したら`ErrorReporter.Report`を1回だけ呼ぶこと。11回目以降は呼ばず、送れたら数え直すこと
  - 本文の決済の期限が日本時間で書かれ、英語の本文に`+09:00`が入ること
  - 本文の照会URLが`baseURL`で始まり、`token`が`Tokens.Verify`を通ること。言語は`purchases.lang`に従うこと
- [ ] 失敗を確かめ、実装し、試験が通ることを確かめる
- [ ] コミットする（`feat(app): add expiry, refund, reconcile and mail loops`）

### タスク14 認証と主催者の操作

- 作る `internal/auth/password.go`、`internal/auth/throttle.go`、`internal/app/organizer.go`
- 試験 それぞれの`_test.go`
- 出力

```go
func HashPassword(pw string) (string, error)       // Argon2id、64MiB、3回、並列4
func VerifyPassword(hash, pw string) (bool, error)
type Limiter struct{ /* ... */ }                   // 同時計算をruntime.NumCPU()までに抑える
func (l *Limiter) Acquire(ctx context.Context) error
type Throttle struct{ /* ... */ }
func (t *Throttle) Allowed(email, ip string, now time.Time) bool
func (t *Throttle) Fail(email, ip string, now time.Time)
// internal/app
func (s *Service) CreateOrganizer(ctx context.Context, email, password string) error // ErrBadInput、ErrDuplicateOrganizer
func (s *Service) Login(ctx context.Context, email, password, ip string) (sessionID string, err error)
func (s *Service) Session(ctx context.Context, sessionID string) (organizerID string, err error)
func (s *Service) Logout(ctx context.Context, sessionID string) error
func (s *Service) CreateEvent(ctx context.Context, organizerID string, in EventInput) (string, error)
func (s *Service) PurgeSessions(ctx context.Context) // 1時間ごとに非同期項目で失効したセッションを消す
func (t *Throttle) Sweep(now time.Time)            // 1分ごとに15分より古い記録を消す
```

- [ ] 次の試験を書く
  - `VerifyPassword`が正しいパスワードで真、1文字違いで偽を返すこと
  - `Throttle`はメールアドレスごとの5回目の失敗の後に拒み、15分後に許すこと。IPアドレスごとは20回目の後に拒むこと
  - `Throttle.Sweep`の後に15分より古い記録が残らないこと
  - `Limiter.Acquire`は空きがないまま`ctx`の期限が過ぎるとエラーを返すこと（HTTP層はこれを503にする）
  - `CreateOrganizer`は`"Alice <a@example.com>"`と11文字のパスワード（`"あいうえおかきくけこさ"`）を`ErrBadInput`にし、12文字は通すこと。大文字違いの2回目は`ErrDuplicateOrganizer`
  - `Login`は存在しないメールアドレスでも`VerifyPassword`と同じ時間をかけること（呼ばれた回数で確かめる）。成功したらセッションIDのSHA-256だけを保存すること
  - `Session`はログインから12時間ちょうどと、最終操作から2時間ちょうどで失効すること。最終操作の更新は前回から5分以上たったときだけ送ること
  - `CreateEvent`の後に`Inventory.Remaining`が定員を返すこと
  - `PurgeSessions`の後に失効したセッションの行がなく、有効な行は残ること
- [ ] 失敗を確かめ、実装し、試験が通ることを確かめる
- [ ] コミットする（`feat(auth): add organizer auth and sessions`）

### タスク15 購入者の画面

- 作る `internal/web/server.go`、`internal/web/public.go`、`internal/web/templates/*.html`、`internal/web/static/htmx.min.js`、`internal/web/static/app.css`
- 試験 `internal/web/public_test.go`（`httptest`）
- 出力 `func NewHandler(d Deps) http.Handler`。`Deps`は`*app.Service`、`i18n.Translator`、`auth.Tokens`、`Clock`、保留の回数制限を持つ

- [ ] htmxの`2.0.8`の`htmx.min.js`を取得し、`internal/web/static/`に置く
- [ ] 次の試験を書く
  - 全応答に仕様のCSPの見出しが付き、`Vary`に`Accept-Language`と`HX-Request`が入ること
  - 別のオリジンの`Origin`を付けた`POST /events/{id}/purchases`が403になること
  - `Accept-Language: en`で英語の題名、見出しなしで日本語の題名を描くこと
  - 購入の`POST`が購入状況の画面へ303で進み、URLの`token`が`Tokens.Verify`を通ること
  - 売り切れと入力の誤りで詳細の画面にエラー文を出すこと
  - 購入状況の部分の応答は`held`の間200、`held`でなくなったら286を返し、`Cache-Control: no-store`が付くこと
  - 照会でメールアドレスかトークンが違うとき、どちらの場合も同じ文を返すこと
  - 同じIPアドレスから1分に11回目の保留の作成が429になり、上限0なら制限しないこと
  - 各画面の`<head>`に仕様のhtmxの設定の`meta`があること
  - 応答に判定した言語の`Content-Language`が付くこと
  - `GET /purchases/{id}?token=...`の後に`access_logs`へ非同期項目が送られ、`path`にクエリ文字列が入らないこと
- [ ] 失敗を確かめ、実装する。テンプレートと静的ファイルは`embed.FS`で読む
- [ ] 試験が通ることを確かめ、コミットする（`feat(web): add buyer pages`）

### タスク16 主催者の画面

- 作る `internal/web/admin.go`、`internal/web/templates/admin_*.html`
- 試験 `internal/web/admin_test.go`

- [ ] 次の試験を書く
  - セッションのない`GET /admin/events`がログインの画面へ戻すこと
  - ログインの成功で`__Host-firstline_session`のCookieが`Secure`、`HttpOnly`、`SameSite=Lax`、`Path=/`で付くこと
  - 6回目の失敗で429を返すこと
  - 他の主催者のイベントの`GET /admin/events/{id}`が404になること
  - イベント作成の後に販売状況の画面へ進み、定員と残数が一致すること
  - ログアウトでセッションの行とCookieが消えること
- [ ] 失敗を確かめ、実装し、試験が通ることを確かめる
- [ ] コミットする（`feat(web): add organizer pages`）

### タスク17 観測

- 作る `internal/observe/logreporter/logreporter.go`、`internal/observe/emf/emf.go`、`internal/observe/health.go`
- 試験 それぞれの`_test.go`
- 出力

```go
func logreporter.New(l *slog.Logger) port.ErrorReporter
func emf.New(addr string) port.MetricsSink
func observe.Snapshot(w *writer.Writer, dbPath string, lastReconcile func() time.Time, certNotAfter func() time.Time, now time.Time) port.HealthSnapshot
func observe.Handler(snap func() port.HealthSnapshot) http.Handler // /healthz
```

- [ ] 次の試験を書く
  - `logreporter`がERRORのJSONを1行書き、`level`が`"ERROR"`であること
  - `emf`がTCPの受け手に、`_aws`の中に`LogGroupName: "/firstline/metrics"`と名前空間`firstline`を持つ1行のJSONを送ること。受け手がいなければエラーを返すこと
  - `/healthz`のJSONが仕様の9項目をすべて持つこと
  - `commit_wait_p99_ms`と`sync_items_per_sec_max_5m`が、5分より古い値を含めないこと
- [ ] 失敗を確かめ、実装し、試験が通ることを確かめる
- [ ] コミットする（`feat(observe): add error reporter, EMF and healthz`）

### タスク18 PostgreSQLとMySQLとデータ移行

- 作る `internal/store/postgres/`、`internal/store/mysql/`（`Open`と`sql/*.sql`）、`internal/migrate/migrate.go`。移行用に`export_<表>`と`import_<表>`のSQLファイルを3方言に足し、PostgreSQLだけに`setval_mail_outbox`を置く。タスク7の名前の集合の試験では`setval_mail_outbox`を除いて比べる
- 試験 `internal/store/postgres/storetest_test.go`、`internal/store/mysql/storetest_test.go`、`internal/migrate/migrate_test.go`（testcontainersで`postgres:16-alpine`と`mysql:8.4`）
- 出力

```go
func postgres.Open(ctx context.Context, dsn string) (*store.Store, error) // synchronous_commitとfsyncがonでなければ失敗
func mysql.Open(ctx context.Context, dsn string) (*store.Store, error)    // innodb_flush_log_at_trx_commitが1でなければ失敗
func migrate.Run(ctx context.Context, from, to *store.Store) error
```

- [ ] 次の試験を書く
  - 両方言で`storetest.Run`が通ること
  - `synchronous_commit = off`のPostgreSQLで`Open`が失敗すること
  - `migrate.Run`がSQLiteからPostgreSQLとMySQLへ全表を移し、行数と金額の合計が一致すること
  - PostgreSQLへ移した後に保留を1件作れること（`mail_outbox.id`の採番が`setval`で合っている）。`mail_outbox`が空のときは`setval`を呼ばないこと
  - 移行先に行があるときに失敗すること
- [ ] 失敗を確かめ、実装し、試験が通ることを確かめる
- [ ] コミットする（`feat(store): add PostgreSQL and MySQL stores and migrate-data`）

### タスク19 組み立て、起動と停止、サブコマンド

- 作る `cmd/firstline/main.go`、`cmd/firstline/config.go`、`cmd/firstline/serve.go`、`cmd/firstline/tlsreload.go`、`cmd/firstline/lock.go`、`cmd/firstline/organizer.go`、`cmd/firstline/reconcile.go`、`cmd/firstline/migrate.go`
- 試験 それぞれの`_test.go`

- [ ] 次の試験を書く
  - `LoadConfig`が仕様の設定の表のすべての環境変数と既定を読み、`FIRSTLINE_TOKEN_KEY`が31バイトか`FIRSTLINE_BASE_URL`が空なら失敗すること。`FIRSTLINE_ERROR_REPORTER=log,log`で2つの報告先へ送ること
  - HTTPの`http.Server`の時間制限が`ReadHeaderTimeout`5秒、`ReadTimeout`10秒、`WriteTimeout`15秒、`IdleTimeout`60秒で、リクエストの期限が10秒であること
  - 起動が仕様の手順1〜8の順に進み、照合が期限切れより先に動くこと（各手順が記録を残す偽の部品で順を比べる）
  - 停止が仕様の手順1〜4の順に進み、3つの`Shutdown`に同じ期限のcontextが渡ること
  - SIGHUPで新しい証明書を読み、読み直しに失敗したら古い証明書を使い続けること
  - ロックを持つプロセスがあるとき、2つ目の`serve`が終了コード1で終わること
  - `create-organizer`が、ソケットがあれば`POST /organizers`を送り、201で0、409で1を返すこと。ソケットがなければロックを取って自分で書くこと
  - `POST /organizers`がソケットの`http.Server`にだけ登録され、HTTPの待ち受けでは404であること
- [ ] 失敗を確かめ、実装し、試験が通ることを確かめる
- [ ] コミットする（`feat(cmd): wire serve and subcommands`）

### タスク20 配備の設定

- 作る `deploy/firstline.service`、`deploy/litestream.service`、`deploy/litestream.yml`、`deploy/cloudwatch-agent.json`、`deploy/logrotate-firstline`、`deploy/logrotate.timer.d/hourly.conf`、`deploy/firstline-cert-deploy`、`scripts/restore.sh`

- [ ] 仕様の配備の節の値で各ファイルを書く
- [ ] macOSには`systemd-analyze verify`がないので、`scripts/check.sh`に`grep`で次を確かめる行を足す。`TimeoutStopSec=30`、`RuntimeDirectory=firstline`、`LogsDirectory=firstline`、`ExecReload=/bin/kill -HUP $MAINPID`
- [ ] `shellcheck scripts/restore.sh deploy/firstline-cert-deploy`が通ることを確かめる
- [ ] `restore.sh`の手順を、`litestream`と`systemctl`を偽のコマンドに置いた一時の`PATH`で実行し、呼ばれる順が仕様の復元の手順1〜7と一致することを確かめる
- [ ] コミットする（`feat(deploy): add systemd, Litestream, CloudWatch and restore files`）

### タスク21 E2E

- 作る `e2e/e2e_test.go`（ビルドタグ`e2e`）
- 試験 `go test -tags e2e -count=1 ./e2e`

- [ ] 次の試験を書く。一時ディレクトリにDB、自己署名の証明書、ソケットを置き、`go build`した実際のバイナリを起動する
  - 主催者を`create-organizer`で作り、ログインしてイベントを作る
  - 購入者が保留を作り、模擬の決済が完了して`purchased`になり、照会で整理番号が見えること
  - 模擬の決済の遅延を保留の期限より長くし、期限切れの後の完了で`refunded`になること
  - SIGTERMで終了コード0で止まり、再起動の後も購入が残っていること
- [ ] 失敗を確かめ（タスク19までが揃っていれば通る。通らなければ原因を直す）、試験が通ることを確かめる
- [ ] `bash scripts/check.sh`がすべて通ることを確かめる
- [ ] コミットする（`test(e2e): cover purchase, refund and restart flows`）

## 計画に入れないもの

- 限界負荷の負荷試験は配備先と同じホストで行う運用の作業なので入れない（ADR-0003）
- 実際の決済サービスとメール送信サービスの実装は作らない（要件書）
- 月次の復元試験は運用の作業なので入れない。手順の試験はタスク20で行う
