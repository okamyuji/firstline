# firstline基本設計書

この文書は`docs/requirements/scope-and-decisions.md`の決定事項を、実装者が聞き返さずに作れる粒度へ落としたものです。決定の理由のうち長く残すべきものは`docs/design/adr/`のADRに分けて書いています。

## 決定表

要件書の「設計書で決めること」に挙がった項目と、設計の途中で決める必要が出た項目を、ここですべて決めます。ADRの列が空でない行はそのADRに理由の詳細があります。

| 決めること | 検討した選択肢 | 採用 | 理由 | ADR |
|---|---|---|---|---|
| ReadDBを読み取り専用にする方法 | `mode=ro`、`_query_only=1` | `file:`で始まる接続文字列に`mode=ro`を付ける | `mode=ro`はすべての書き込みを`SQLITE_READONLY`で拒み、存在しないファイルも作りません。`_query_only`は後のPRAGMAで解除できます | ADR-0001 |
| PRAGMAの渡し方 | 接続文字列の`_pragma`、接続後の`Exec` | 接続文字列の`_pragma`と短縮キー | `database/sql`の接続プールが新しく開くどの接続にも同じ設定が入ります | ADR-0001 |
| WriteDBの接続数 | 1、複数 | 1本にし、書き込みは専用のゴルーチン1つが行う | SQLiteの書き込みは同時に1つだけなので、待ちをSQLiteのロックでなくGoのチャネルで管理します | ADR-0001 |
| グループコミットの条件 | 件数のみ、時間のみ、両方 | 64件たまるか、最初の1件から15ms経ったときにコミットする。同期項目が入ったら、チャネルにある項目を待たずに取り出してその場でコミットする | ログの書き込み回数を減らしつつ、購入確定の応答を待たせないためです | ADR-0002 |
| 課金ログの書き方 | 非同期、状態遷移と同じトランザクション | 購入確定と払い戻しの状態遷移と同じトランザクションで書く | 課金ログは失えないためです | ADR-0002 |
| チェックポイントの担当 | SQLiteの自動、アプリ、Litestream | Litestreamに任せ、アプリは`wal_autocheckpoint=0`にする | アプリが割り込んでチェックポイントするとLitestreamがWALの更新を取りこぼし、全体のスナップショットを取り直すためです | ADR-0001 |
| 時刻の保存形式 | ドライバの既定の文字列、DBの時刻型、整数 | UTCのUnixミリ秒を`BIGINT`の列に置き、Goで変換する | SQLite、PostgreSQL、MySQLで同じ比較のSQLを使えます | ADR-0004 |
| 保留の期限 | 5分、10分、15分 | 10分 | 決済の入力に要する時間と、売り切れ時に席が戻るまでの待ちの釣り合いを取るためです | なし |
| 在庫の復元 | 永続化しない、起動時に再計算する | 起動時にWriteDBから再計算する | 保留と購入は同期で書くので、WriteDBが在庫の正本になります | ADR-0001 |
| 決済の完了通知の照合 | `payment_id`、購入ID | 購入ID | 購入IDは`Payment.Start`を呼ぶ前に決まっているので、`payment_id`を保存する前に通知が届いても照合できます | なし |
| 完了通知を取りこぼしたときの回復 | 回復しない、完了済みの決済を照会する | `Payment.Completed`で完了済みの決済を照会し、通知と同じ処理に流す | プロセスが落ちて通知を失っても、払ったのに席がない状態を残さないためです | なし |
| 主催者セッションの保存先 | メモリ、WriteDBの表、署名付きCookie | WriteDBの`sessions`表 | 再起動で消えず、ログアウトでその場で無効にできるためです | なし |
| 主催者セッションの有効期限 | 固定期限のみ、無操作期限のみ、両方 | ログインから12時間、または2時間操作がなければ失効する | 共用の端末で開いたままになる時間を短くするためです | なし |
| 主催者の作り方 | 登録画面、CLI | CLIの`create-organizer` | 主催者は運用者が作るので、外へ開いた登録画面は要りません | なし |
| 購入照会トークンの方式 | 乱数を表に保存、HMAC-SHA256、Ed25519 | HMAC-SHA256の署名付きトークン | 検証するのはサーバーだけなので公開鍵は要りません。表を引かずに検証できます | なし |
| 購入照会トークンの有効期限 | 固定日数、イベント基準 | イベント終了の30日後まで | 来場後の問い合わせと払い戻しの確認に使えるようにするためです | なし |
| 主催者ログインの試行制限 | なし、メモリの計数、表の計数 | メモリで数え、メールアドレスごとに15分で5回、IPアドレスごとに15分で20回失敗したら15分止める | 単一ノードなので共有は不要です。再起動で数がなくなるのは受け入れます | なし |
| TLSの終端と送信元のIPアドレス | 前段のプロキシ、ALBとACM、firstline自身 | firstlineが標準ライブラリの`http.Server`で終え、IPアドレスは`Request.RemoteAddr`から取る | 前段を置かなければ、転送用の見出しを信じるかどうかの判断が要らず、ALBの月額もかかりません | なし |
| 証明書の更新 | 手作業、certbot、firstlineに内蔵 | Let's Encryptの証明書をcertbotが自動で更新し、firstlineはSIGHUPで読み直す | 更新を忘れて販売サイトにつながらなくなる事態をなくし、更新で再起動もしないためです | なし |
| 利用者向け文字列の持ち方 | JSONファイル、Goのmap、`golang.org/x/text/message` | 言語ごとのGoのmapに置き、言語の照合は`golang.org/x/text/language`の`Matcher`で行う | 翻訳漏れをテストで検出でき、読み込みの失敗が起こりません | なし |
| メールの送り方 | 処理中に直接送る、送信待ちの表を経由する | 状態の変更と同じトランザクションで`mail_outbox`表へ書き、送信用のゴルーチンが送る | 状態だけ変わってメールが送られない状態をなくすためです | なし |
| CSRFの防ぎ方 | トークン、`Origin`の検証 | 標準ライブラリの`http.CrossOriginProtection`をすべての経路にかける | 購入者にはセッションがないので、トークン方式を共通に使えません | なし |
| 規模拡大の判断 | 感覚で決める、測れる値で決める | ADR-0003の基準に従う | 移る時期を測定結果で決めるためです | ADR-0003 |
| 画面の技術 | React、Vue、`html/template`とhtmx | `html/template`でサーバーが描画し、部分の更新はhtmxの2.0.8以上で行う。htmxのファイルはバイナリに同梱する | ビルドの工程とnpmの依存が要らず、言語の切り替えと`http.CrossOriginProtection`をサーバー側だけで扱えるためです | なし |
| 観測の構成 | CloudWatch、専用のサービス、OpenTelemetry | ログはsystemdがファイルへ追記したものをCloudWatch Agentで、指標はEMFでCloudWatchへ送る。エラーは`ErrorReporter`の差し替え口から報告する | 初期の費用を抑え、移る時期は測れる値で決めるためです | ADR-0005 |

## 決定事項との対応表

要件書の各決定事項を、どの節で設計しているかの対応です。

| 決定事項 | 設計の場所 |
|---|---|
| 移行の範囲 | 構成、設定とCLI、品質ゲート、ADR-0003 |
| 在庫の判定 | 在庫、ADR-0003 |
| 決済 | 決済 |
| 期限切れ後の決済完了 | 状態遷移 |
| 購入確定の永続性 | グループコミット、ADR-0002 |
| CQRS | 構成、ADR-0004 |
| DI | 構成、ADR-0004 |
| SQL | 構成、ADR-0004 |
| Goの書き方 | 品質ゲート |
| 状態遷移 | 状態遷移 |
| SQLiteドライバ | 接続設定、ADR-0001 |
| 認証 | 認証 |
| 画面の範囲 | 画面 |
| 画面の技術 | 画面 |
| メール | メール |
| 文字列の言語 | 文字列の言語 |
| 観測 | 観測、配備、ADR-0005 |
| 品質ゲート | 品質ゲート |

要件書の「設計書で決めること」の1項目めにある設定値は、次の節に書いています。

| 設定値 | 値 | 設計の場所 |
|---|---|---|
| ReadDBの`mode=ro` | `file:`で始まる接続文字列に付ける | 接続設定 |
| WAL | WriteDBが`_journal_mode=WAL`で開く。ReadDBは指定しない | 接続設定 |
| mmap | ReadDBの`mmap_size`を268435456バイト（256MiB）にする | 接続設定 |
| `cache_size` | ReadDBは`-65536`（64MiB）、WriteDBは`-16384`（16MiB）にする。負の値はKiB単位です | 接続設定 |
| `wal_autocheckpoint=0` | WriteDBに設定する | 接続設定 |
| 容量8192のチャネル | 書き込み項目を受けるチャネル | グループコミット |
| 64件または15ms | 書き込みゴルーチンのコミット条件 | グループコミット |
| Drain | 停止時の順序 | 起動と停止 |
| systemd | `firstline.service`と`litestream.service` | 配備 |
| embed.FS | テンプレートと静的ファイルを同梱する | 画面 |
| Litestreamの設定と復元スクリプト | `/etc/litestream.yml`と`scripts/restore.sh` | 配備 |

## 構成

firstlineは1台のLinuxホストで動く単一のGoバイナリです。HTTPの処理、書き込みゴルーチン、期限切れの処理、払い戻しの再試行、決済の照合、メールの送信が同じプロセスにあります。Litestreamは別のプロセスとして動き、WALをCloudflare R2へ送ります。

各層はinterfaceで受け取り、`cmd/firstline`の組み立て関数1か所で注入する構成です。

| 層 | 置き場 | 役割 |
|---|---|---|
| HTTP | `internal/web` | 経路、テンプレートの描画、エラー文の変換、言語の判定 |
| ユースケース | `internal/app` | 保留、購入確定、期限切れ、払い戻し、照会、照合、主催者の操作 |
| ドメイン | `internal/domain` | 購入の状態遷移、在庫の数え方、エラーの型 |
| 差し替え口 | `internal/port` | `Store`、`Inventory`、`Payment`、`PaymentNotifier`、`Mailer`、`ErrorReporter`、`MetricsSink`のinterface |
| 書き込み | `internal/store/writer` | グループコミットを行う書き込みゴルーチン。3つの方言で共通に使う |
| 実装 | `internal/store/sqlite`、`internal/store/postgres`、`internal/store/mysql`、`internal/inventory`、`internal/payment/fake`、`internal/mail/fake`、`internal/observe/logreporter`、`internal/observe/emf` | 差し替え口の実装 |
| SQL | `internal/store/<方言>/sql/*.sql` | 方言ごとのSQLファイル。`embed.FS`で読み、Bunの`db.NewRaw`で実行する |

`Store`は方言ごとに書き込み用と読み取り用の`*bun.DB`を持ち、SQLファイルを実行するメソッドを提供します。書き込みゴルーチンは`Store`の書き込み用の`*bun.DB`でトランザクションを開き、各項目が持つ関数をその中で実行します。PostgreSQLとMySQLでも同じ書き込みゴルーチンを使うので、グループコミットと同期項目の規則は方言によらず同じです。

書き込みはWriteDB、メモリ在庫、グループコミットを通ります。読み取りはReadDBと読み取り用の表`event_sales`を使います。コミットした内容が次の読み取りから見えるのは、両者が同じファイルを開いているためです。

### 表

時刻の列はすべてUTCのUnixミリ秒の`BIGINT`です。`organizers.id`、`events.id`、`purchases.id`は、アプリが`crypto/rand`の16バイトをbase64urlにして作る文字列で、DBの採番を使いません。DBが採番する列は`mail_outbox.id`だけです。この列はPostgreSQLでは`bigint GENERATED BY DEFAULT AS IDENTITY`、MySQLでは`BIGINT AUTO_INCREMENT`、SQLiteでは`INTEGER PRIMARY KEY`にします。

| 表 | 主な列 | 書く時機 |
|---|---|---|
| `organizers` | `id`、`email`（一意）、`password_hash` | `create-organizer`の実行時に同期 |
| `sessions` | `id_hash`、`organizer_id`、`created_at`、`last_seen_at` | ログイン時は同期、最終操作時刻は非同期 |
| `events` | `id`、`organizer_id`、`title_ja`、`title_en`、`starts_at`、`ends_at`、`capacity`、`price`、`sale_starts_at`、`next_ticket_number` | イベント作成時は同期 |
| `purchases` | `id`、`event_id`、`email`、`lang`、`quantity`、`amount`、`state`、`expires_at`、`payment_id`、`checkout_url`、`ticket_number_from`、`updated_at` | 状態が変わるたびに同期 |
| `event_sales` | `event_id`、`held`、`sold`、`remaining`、`refunds` | イベント作成の同期項目で`remaining`を定員にして作る。以後は`purchases`と同じトランザクション |
| `orphan_refunds` | `payment_id`（主キー）、`purchase_id`、`amount`、`state`、`updated_at` | 記録のない購入への完了通知と、その払い戻しの成功の同期項目 |
| `mail_outbox` | `id`（自動採番の整数）、`purchase_id`、`kind`、`lang`、`sent_at` | 作成は`purchases`と同じトランザクション。`sent_at`は同期 |
| `billing_logs` | `purchase_id`、`payment_id`、`kind`（`charge`か`refund`）、`amount`、`at` | 購入確定と払い戻しの状態遷移と同じトランザクション |
| `access_logs` | `method`、`path`、`status`、`duration_ms`、`at` | 非同期（グループコミット） |

`access_logs.path`には`Request.URL.Path`だけを入れ、クエリ文字列は入れません。購入照会トークンがクエリに入るためです。

購入確定は`charge`、払い戻しの完了は`refund`の課金ログを書きます。期限切れか決済の失敗の後に完了した決済は`charge`を書かずに払い戻すので、`refund`の行だけが残ります。`billing_logs.purchase_id`と`orphan_refunds.purchase_id`には外部キーを付けません。購入の記録がない決済についても払い戻しの課金ログを書くためです。

## 接続設定

`modernc.org/sqlite`は、名前が`file:`で始まるときだけSQLite自身のURIパラメータ（`mode`など）を解釈します。普通のファイル名に`mode=ro`を付けても黙って無視され、読み書きで開かれます。このためReadDBとWriteDBの接続文字列は、どちらも`file:`で始まる形です。

WriteDBの接続文字列は次のとおりです。

```text
file:/var/lib/firstline/firstline.db?_journal_mode=WAL&_synchronous=FULL&_busy_timeout=5000&_foreign_keys=1&_txlock=immediate&_pragma=wal_autocheckpoint(0)&_pragma=cache_size(-16384)
```

ReadDBの接続文字列は次のとおりです。

```text
file:/var/lib/firstline/firstline.db?mode=ro&_busy_timeout=5000&_pragma=mmap_size(268435456)&_pragma=cache_size(-65536)
```

| 項目 | WriteDB | ReadDB |
|---|---|---|
| `SetMaxOpenConns` | 1 | 8 |
| `SetMaxIdleConns` | 1 | 8 |
| 開く順 | 先に開き、`PingContext`で接続してWALにする | WriteDBの`PingContext`が成功した後で開く |

ReadDBには`_journal_mode=WAL`を付けません。ドライバの文書によれば、WALになる前のデータベースへ`mode=ro`の接続でWALへの切り替えを求めると、書き込みが要るため開くこと自体が`SQLITE_READONLY`で失敗します。SQLiteは書き込み権限がない接続でも、`-shm`と`-wal`のファイルがあるか、ディレクトリに書き込み権限があればWALのデータベースを読めます（[WALの読み取り専用データベース](https://www.sqlite.org/wal.html)）。`sql.Open`は接続を作らないので、WriteDBの`PingContext`でWALにしてからReadDBを開く順序が要ります。

接続文字列は組み立て関数の中の定数から作り、外部の入力を混ぜません。`_pragma`の値は検証されずにSQLとして実行されると、ドライバの文書に書かれているためです。起動時に`sqlite.StrictPragmas(true)`も呼び、1つの`_pragma`に複数の文が入っていたら開くのを失敗させます。

PostgreSQLとMySQLでは、書き込み用と読み取り用の`*bun.DB`を同じ接続先で作ります。購入確定がディスクへの書き込みを待つ設定は次のとおりです。firstlineは起動時に値を読み、違っていたら起動しません。

| DB | 設定 | 値 | 一次情報 |
|---|---|---|---|
| PostgreSQL | `synchronous_commit` | `on`（既定） | [WALの設定](https://www.postgresql.org/docs/current/runtime-config-wal.html) |
| PostgreSQL | `fsync` | `on`（既定） | [WALの設定](https://www.postgresql.org/docs/current/runtime-config-wal.html) |
| MySQL | `innodb_flush_log_at_trx_commit` | `1`（既定） | [InnoDBの設定](https://dev.mysql.com/doc/refman/8.0/en/innodb-parameters.html) |

## グループコミット

WriteDBへの書き込みはすべて1つの書き込みゴルーチンを通ります。HTTPの処理などの呼び出し側は書き込み項目を容量8192のチャネルへ送ります。

| 項目の種類 | 例 | 呼び出し側の待ち方 |
|---|---|---|
| 同期項目 | 保留の作成、`payment_id`の記録、購入確定、決済の失敗、期限切れ、払い戻しの開始と完了、メールの送信済みの記録、イベント作成、主催者の作成、ログイン、ログアウト | コミットの完了を待ってから応答する |
| 非同期項目 | アクセスログ、セッションの最終操作時刻、失効したセッションの削除 | 送ったら待たない |

課金ログは購入確定と払い戻しの同期項目の中で書くので、独立した項目にはなりません。

書き込みゴルーチンは次の規則でコミットします。

1. 最初の項目を受け取った時点から15msを計ります。
2. 64件たまるか15msが過ぎたら、たまった項目を1つのトランザクションでコミットします。
3. 同期項目を受け取ったら、チャネルにすでにある項目を待たずに取り出します。たまった項目が64件に達するか、チャネルが空になった時点でコミットします。
4. コミットの結果を、同期項目それぞれの返信用チャネルへ返します。

WriteDBは`synchronous=FULL`なので、コミットはfsyncの完了まで戻りません。購入確定の応答はこの後に返るため、応答を受け取った購入は電源断でも失われません。販売開始の直後のように同期項目が続けて来るときは、規則3によって1回のコミットに最大64件がまとまります。

書き込みゴルーチンは項目ごとにトランザクションの中でセーブポイントを作ります。項目の書き込みが失敗したときはその項目のセーブポイントまで巻き戻し、エラーを返すのもその項目だけです。ほかの項目は残し、バッチ全体を1回でコミットします。売り切れの直後のように失敗が多いときも、fsyncはバッチごとに1回です。非同期項目の失敗は返す相手がないので、`ErrorReporter`で報告します。

WriteDBのコミットがI/Oエラー（`SQLITE_IOERR`の系統）を返したときは別の扱いにします。fsyncが失敗するとWALにコミットが残ったかどうかが分からず、コミット済みの購入をエラーとして返すおそれがあるためです。書き込みゴルーチンはどの項目にも結果を返さず、席も戻さずにプロセスを異常終了させます。systemdが再起動し、起動時の在庫の作り直しでWriteDBの内容に合わせます。

### 期限と待ち方

HTTPのリクエストは10秒の期限を持ちます。`http.Server`の時間制限は`ReadHeaderTimeout`を5秒、`ReadTimeout`を10秒、`WriteTimeout`を15秒、`IdleTimeout`を60秒にします。

| 場面 | 振る舞い |
|---|---|
| チャネルが満杯で、同期項目を送れない | リクエストの期限まで待ちます。期限を過ぎたら項目を送らずに503を返し、確保した席を`Inventory.Release`で戻します |
| 同期項目を送った後 | リクエストの期限が過ぎても、クライアントが切断しても、コミットの結果を受け取るまで待ちます。席を戻すのは、結果がエラーだったときだけです |
| HTTPの外の処理（期限切れ、払い戻し、照合、メール）が送れない | 停止の合図を受けるまで待ちます |
| 非同期項目を送れない | 捨てて、捨てた件数を数えます |

送った後で期限を理由に席を戻すと、その後でコミットが成功したときに事前判定の残数がDBより多くなります。上の表の2行目はこのずれを防ぐための規則です。

## 起動と停止

起動は次の順で進みます。

1. SIGHUPとSIGTERMの受け口を作り、`FIRSTLINE_LOCK_PATH`を`flock`で取ります。ロックを取れなければ起動しません。続けて設定を検証し、TLSの証明書と秘密鍵の読み込みと`FIRSTLINE_TOKEN_KEY`の長さのどちらかが失敗したら起動しません。
2. WriteDBを開き、`PingContext`で接続してWALにします。
3. ReadDBを開き、`PingContext`で確かめます。
4. 書き込みゴルーチンを始めます。
5. WriteDBから在庫を作り直します。
6. 決済の照合、期限切れの処理、払い戻しの再試行の順に1回ずつ動かします。決済の照合はこの回だけ、24時間前からの完了を照会します。照合を先に動かすのは停止中に期限を過ぎた保留のうち、期限内に支払いが終わっていたものを期限切れにする前に購入済みにするためです。
7. 期限切れの処理、払い戻しの再試行、決済の照合、メールの送信を定期の処理として始めます。
8. 運用者向けの待ち受け、`FIRSTLINE_ADMIN_SOCK`、HTTPの受付を始めます。

在庫を作り直す前にHTTPを受け付けると、作り直しがそれまでの確保を上書きします。HTTPの受付を最後に置くのはこのためです。

停止は次の順で進みます（Drain）。

1. SIGTERMを受けたら、HTTP、運用者向けの待ち受け、`FIRSTLINE_ADMIN_SOCK`の3つの`http.Server`の`Shutdown`を並行に呼んで新しい接続の受付を止め、処理中のリクエストの終わりを待ちます。
2. 決済の完了通知の受付を止め、`Payment`の実装に通知の送信をやめさせます。期限切れの処理、払い戻しの再試行、決済の照合、メールの送信も止め、終わるまで待ちます。
3. 書き込みゴルーチンに停止の合図を送ります。書き込みゴルーチンはチャネルに残った項目をすべてコミットしてから終わります。
4. ReadDBとWriteDBを閉じます。

書き込みのチャネルは閉じません。停止の合図の後に送ろうとした呼び出し側は、`writer.ErrShuttingDown`を受け取ります。`Shutdown`が期限を過ぎてハンドラが動き続けていても、閉じたチャネルへの送信で落ちることはありません。

systemdの`TimeoutStopSec`は30秒にします。3つの`Shutdown`には共通の20秒の期限を持つcontextを渡し、残りの10秒を手順2から4に充てます。

## 在庫

在庫は`Inventory`の差し替え口から使います。

```go
type Inventory interface {
	Add(ctx context.Context, eventID string, capacity int) error
	Reserve(ctx context.Context, eventID string, quantity int) error
	Release(ctx context.Context, eventID string, quantity int) error
	Remaining(ctx context.Context, eventID string) (int, error)
}
```

席の数の正本は`event_sales.remaining`です。保留を書くSQLは`remaining >= ?`を条件にこの列を減らし、更新件数が0なら`domain.ErrSoldOut`を返します。この条件はどの在庫実装でも同じで、二重販売を最後に止める場所になります。

メモリ実装はこの判定の前に置く事前判定です。イベントごとの残数を`sync.Mutex`で守り、`Reserve`は残数が足りなければ`domain.ErrSoldOut`を返して書き込みゴルーチンへ項目を送りません。販売開始の直後に売り切れた後の購入を、DBの往復なしで断るためです。DB実装の`Reserve`と`Release`は何もせず、判定を`event_sales.remaining`の条件だけに任せます（ADR-0003）。

保留の作成は次の順で進みます。

1. `Inventory.Reserve`で席を確保します。
2. 保留を同期項目として書きます。
3. 結果がエラーなら`Inventory.Release`で席を戻し、エラーを返します。

席を戻す遷移（`released`、`expired`、`held`からの`refunding`）では、コミットが成功した後に`Inventory.Release`を呼びます。コミットの前に戻すとコミットが失敗したときに事前判定の残数がDBより多くなるので、コミットの前には戻しません。

イベント作成の同期項目がコミットされたら、`Inventory.Add`で定員を登録します。起動時にはWriteDBから在庫を作り直し、イベントごとに`Add`を呼びます。このときの残数は定員から状態が`held`と`purchased`の購入の枚数の合計を引いた数です。保留と購入確定は応答の前にfsyncしているため、応答を返した席はすべてこの計算に入ります。

## 決済

決済は`Payment`の差し替え口から使います。

```go
type Payment interface {
	Start(ctx context.Context, purchaseID string, amount int) (paymentID, checkoutURL string, err error)
	Refund(ctx context.Context, paymentID string) error
	Completed(ctx context.Context, since time.Time) ([]Completion, error)
}

type Completion struct {
	PurchaseID string
	PaymentID  string
	Amount     int
	At         time.Time
}

type PaymentNotifier interface {
	Completed(ctx context.Context, c Completion) error
	Failed(ctx context.Context, purchaseID, paymentID string) error
}
```

`Start`はエラーを返さないとき、空でない`paymentID`を返す契約とします。どの実装でも`Refund`は同じ`payment_id`に何度呼ばれても成功を返し、払い戻しを1回だけ行う契約とします。`Completion.At`は決済サービスが決済を完了した時刻で、送り直しや照合で通知が遅れても変わりません。完了の通知は照合と同じ`Completion`を`PaymentNotifier.Completed`に渡します。模擬実装は決済を完了させた時刻を、実際の決済サービスの実装はWebhookのイベントが示す完了の時刻を入れます。

購入の流れは次のとおりです。

1. `held`の保留をコミットします（在庫の節の手順）。
2. 次の手順は`Payment.Start`に購入IDと`purchases.amount`を渡して決済を始めることです。返った`checkoutURL`は手順3で`purchases.checkout_url`に書き、空でなければ購入状況の画面と保留の通知に決済を続けるリンクとして出します。模擬実装は空を返します。
3. `payment_id`が返ったら、遷移表の「`payment_id`の記録」の同期項目を送ってください。`purchases.payment_id`が空なら、購入がすでに別の状態に進んでいても書きます。記録がエラーで終わっても購入は`held`のまま残し、購入状況の画面へ進めます。その場合は完了通知が`purchases.payment_id`を書きます。
4. `Start`がエラーを返したら、購入を`released`にして席を戻し、決済を始められなかったことを画面に出します。

決済の完了と失敗を購入IDと`payment_id`付きで`PaymentNotifier`へ知らせるのは、`Payment`の実装の役目です。照合は購入IDで行うので、手順3の前に通知が届いても突き合わせられます。`PaymentNotifier`は状態の変更がコミットされた後にだけ成功を返し、`Payment`の実装は成功を受け取るまで5秒ごとに同じ通知を送り直す仕組みです。同じ通知が2回以上届いても結果が変わらないのは、遷移表がそう決めているためです。

プロセスが落ちて通知を失った場合に備えて、決済の照合を30秒ごとに動かします。照合は`Payment.Completed`で15分前からの完了済みの決済を取り、1件ずつ完了通知と同じ処理に流します。15分という幅は期限中の保留がすべて照合の範囲に入るように、保留の期限10分より長く取った値です。

模擬実装`internal/payment/fake`は、`Start`の後に設定した遅延で完了か失敗を通知する実装です。完了した決済はメモリに残し、`Completed`で返します。テストでは遅延を保留の期限より長くでき、期限切れ後の完了通知を再現できます。`Refund`は同じ`payment_id`に何度呼ばれても成功を返す作りです。Stripeの実装は作りません。実際の決済サービスを使う実装はWebhookで受けた通知を同じ`PaymentNotifier`へ渡す形です。

## 状態遷移

購入`purchases`の状態は次のとおりです。

| 状態 | 意味 | 席を数えるか | 終端か |
|---|---|---|---|
| `held` | 席を確保し、決済を待っている | 数える | いいえ |
| `purchased` | 決済が完了し、整理番号が付いた | 数える | はい |
| `released` | 決済を始められなかったか、決済が失敗し、席を戻した | 数えない | いいえ |
| `expired` | 決済の完了前に保留の期限が過ぎ、席を戻した | 数えない | いいえ |
| `refunding` | 席がない購入に決済の完了が届き、払い戻している | 数えない | いいえ |
| `refunded` | 払い戻しが終わった | 数えない | はい |

遷移は次の表だけです。

| 元の状態 | 契機 | 条件 | 先の状態 | 同じトランザクションで行うこと |
|---|---|---|---|---|
| なし | 保留の作成 | 販売開始日時以後でイベントの開始前、枚数が1〜4、`event_sales.remaining`が枚数以上、同じメールアドレスで同じイベントの`held`がない | `held` | `purchases.amount`に価格と枚数の積を書く。`event_sales`の`held`を増やし`remaining`を減らす。保留の通知のメールを`mail_outbox`へ入れる |
| どの状態でも | `payment_id`の記録（購入の流れの手順3） | `purchases.payment_id`が空 | 変えない | `payment_id`と`checkout_url`を書く |
| どの状態でも | `payment_id`の記録（購入の流れの手順3） | `purchases.payment_id`が同じ値 | 変えない | なし。成功を返す |
| どの状態でも | `payment_id`の記録（購入の流れの手順3） | `purchases.payment_id`が違う値 | 変えない | なし。`ErrorReporter`で報告し、エラーを返す |
| `held` | `Start`の失敗、決済の失敗通知 | なし | `released` | `event_sales`の`held`を減らし`remaining`を増やす |
| `held` | 決済の完了通知 | 決済の完了時刻（`Completion.At`）が`expires_at`より前 | `purchased` | `purchases.payment_id`が空なら通知の値を書く。整理番号を付け、`event_sales`の`held`を減らし`sold`を増やす。課金ログに`purchases.amount`を書き、`Completion.Amount`と違えば`ErrorReporter`で報告する。購入確定のメールを`mail_outbox`へ入れる |
| `held` | 決済の完了通知 | 決済の完了時刻が`expires_at`以後 | `refunding` | `purchases.payment_id`が空なら通知の値を書く。`event_sales`の`held`を減らし`remaining`を増やす。`Completion.Amount`が`purchases.amount`と違えば`ErrorReporter`で報告する。払い戻しのメールを`mail_outbox`へ入れる |
| `held` | 期限切れの処理 | 現在時刻が`expires_at`の1分後以後 | `expired` | `event_sales`の`held`を減らし`remaining`を増やす |
| `expired`、`released` | 決済の完了通知 | なし | `refunding` | `purchases.payment_id`が空なら通知の値を書く。`Completion.Amount`が`purchases.amount`と違えば`ErrorReporter`で報告する。払い戻しのメールを`mail_outbox`へ入れる |
| `refunding` | 払い戻しの成功 | なし | `refunded` | 課金ログに`purchases.amount`を書き、`event_sales`の`refunds`を増やす |
| `purchased`、`refunding`、`refunded` | 決済の完了通知 | なし | 変えない | なし。成功を返す |
| `held`以外 | 決済の失敗通知 | なし | 変えない | なし。成功を返す |
| 購入の記録がない | 決済の完了通知 | なし | なし | `orphan_refunds`に`payment_id`を主キーとして状態`refunding`の行を作る。`purchase_id`と`amount`は`Completion`から取る。行がすでにあれば何もしない。成功を返す |
| 購入の記録がない | 決済の失敗通知 | なし | なし | なし。成功を返す |
| `orphan_refunds`の`refunding` | 払い戻しの成功 | なし | `orphan_refunds`の`refunded` | 課金ログに`orphan_refunds.amount`を書く |

表にない組み合わせは`domain.ErrInvalidTransition`になります。

状態を変えるSQLは元の状態を`WHERE state = ?`の条件に入れます。書き込みゴルーチンは1つなので、同じ購入への完了通知と期限切れの処理が同時に適用されることはありません。後になった側は更新件数0を受け取り、購入を読み直して上の表を引き直します。

この設計で成り立つ性質は次のとおりです。

1. 二重販売がない イベントごとに、`held`と`purchased`の枚数の合計は定員を超えません。
2. 払ったのに席がない状態が残らない 決済が完了した購入は、最終的に`purchased`か`refunded`のどちらかになります。購入の記録がない決済も`orphan_refunds`で最終的に`refunded`になります。
3. 席を戻すのは1回だけ 席を戻す遷移は`held`から出る遷移だけです。

TLA+の仕様は`spec/Purchase.tla`、TLCの設定は`spec/Purchase.cfg`です。1と3は不変条件として、2は活性として書きます。活性の前提は期限切れの処理と払い戻しの再試行と決済の照合が弱公平に動くことと、`Payment.Refund`が最終的に成功することです。

期限切れの処理は1秒ごとに、`expires_at`の1分後を過ぎた`held`の購入を探して期限切れの項目を送ります。1分の猶予は期限の直前に完了した決済の通知が、送り直しで遅れて届いても間に合うように置いたものです。払い戻しの再試行は30秒ごとに`refunding`の購入と`orphan_refunds`の`refunding`の行を探し、`Payment.Refund`を呼ぶ処理です。

## 認証

主催者はArgon2idで保存したパスワードとセッションCookieで認証します。Argon2idの引数は[RFC 9106](https://www.rfc-editor.org/rfc/rfc9106#section-4)の2番目の推奨値で、メモリ64MiB、反復3回、並列4です。同時に計算するArgon2idの数はCPU数までに抑え、空きを待つ間にリクエストの期限が過ぎたら503を返します。多数のログインが同時に来てもメモリが尽きないようにするためです。

| 項目 | 値 |
|---|---|
| Cookie名 | `__Host-firstline_session` |
| Cookieの属性 | `Secure`、`HttpOnly`、`SameSite=Lax`、`Path=/` |
| セッションIDの作り方 | `crypto/rand`の32バイトをbase64urlにする |
| 保存する値 | セッションIDのSHA-256。IDそのものは保存しない |
| 有効期限 | ログインから12時間、または2時間操作がないとき |
| 最終操作時刻の更新 | 前回の更新から5分以上たったときだけ、非同期項目で書く |
| 失効したセッションの削除 | 1時間ごとに非同期項目で消す |
| ログアウト | `sessions`の行を同期項目で消し、Cookieを消す |

`/admin/login`以外の`/admin/`の経路は、有効なセッションがなければログインの画面へ戻す決まりです。`/admin/events/{id}`は、イベントの`organizer_id`がセッションの主催者と一致しなければ404を返します。

ログインの失敗の回数はメモリで数え、メールアドレスごとに15分で5回、IPアドレスごとに15分で20回失敗したら、その後の15分はログインを受け付けず429を返します。15分より古い記録は1分ごとに消すので、メモリが際限なく増えることはありません。メールアドレスが存在しない場合も、存在する場合と同じ時間をかけてArgon2idを計算します。応答時間からアカウントの有無を推測させないためです。

購入者はアカウントを作りません。購入を照会するときは、購入ごとの署名付きトークンを使います。

| 項目 | 値 |
|---|---|
| 署名方式 | HMAC-SHA256 |
| 署名する内容 | 購入ID、有効期限のUnix時刻 |
| 形式 | `base64url(購入ID.有効期限).base64url(署名)` |
| 有効期限 | イベント終了の30日後 |
| 鍵 | 環境変数`FIRSTLINE_TOKEN_KEY`の32バイト以上の値。起動時に長さを確かめ、足りなければ起動しない |
| 比較 | `hmac.Equal` |

保留の作成はIPアドレスごとに1分で10回までにします。上限は`FIRSTLINE_HOLD_RATE_PER_MIN`で変えられ、0にすると制限を外します。0を使うのは負荷試験のときだけです。IPアドレスは`Request.RemoteAddr`から取ります。firstlineは前段のプロキシを置かずに直接受けるので、転送用の見出しは読みません。

## 画面

画面はサーバーが`html/template`で描画します。部分の更新にはhtmxの2.0.8以上を使い、htmxのファイルは`internal/web/static/`に置きます。テンプレートと静的ファイルは`embed.FS`でバイナリに同梱し、外部のCDNは使いません。すべての経路に`http.CrossOriginProtection`をかけ、別のオリジンからのPOSTを拒みます。

| 項目 | 設定 |
|---|---|
| CSP | `Content-Security-Policy: default-src 'self'; frame-ancestors 'none'`を全応答に付け、inlineのscriptとstyleを使わない |
| htmxの設定 | 各画面の`<head>`に`<meta name="htmx-config" content='{"includeIndicatorStyles": false, "allowEval": false, "selfRequestsOnly": true, "historyRestoreAsHxRequest": false}'>`を置く。htmxがinlineのstyleを足さず、evalを使う機能も止まるので、CSPを`'self'`だけにできる。処理中の印を出すときは、`.htmx-indicator`のCSSを`internal/web/static/`に置く |
| 部分の応答 | htmxの要求（`HX-Request`の見出しがある要求）には、画面全体でなく更新する部分のHTMLだけを返す。同じURLで全体と部分を返し分けるので、`Vary`に`HX-Request`を入れる |
| キャッシュ | 照会トークンを含む画面とその部分の応答には`Cache-Control: no-store`を付ける |
| 取り直しの停止 | 購入状況の部分の応答は、状態が`held`の間は200、`held`でなくなったら286で返す。htmxは286を受け取ると取り直しを止める |

[htmxの文書](https://github.com/bigskysoftware/htmx/blob/v2.0.8/www/content/docs.md)によれば、`allowEval`を`false`にすると`hx-on:`の属性とイベントのフィルタが使えなくなります。firstlineの画面はこれらを使いません。

| 画面 | 経路 | 主な項目 | 次の画面 |
|---|---|---|---|
| 一覧 | `GET /` | 販売中と販売前のイベント、開催日時、残数 | 詳細 |
| 詳細 | `GET /events/{id}` | 題名、開催日時、価格、残数、購入の入力欄（メールアドレス、枚数1〜4） | 購入 |
| 購入 | `POST /events/{id}/purchases` | なし。保留を作って決済を始める | 購入状況 |
| 購入状況 | `GET /purchases/{id}?token=` | 状態、整理番号、枚数、期限。`held`の間は`checkout_url`があれば決済を続けるリンクと、決済の期限と、期限までに決済を終えないと席は確保されないことを出し、htmxの`hx-trigger="every 5s"`で状態の部分だけを取り直す | なし |
| 照会 | `GET /lookup`、`POST /lookup` | メールアドレス、トークン | 購入状況 |
| 主催者ログイン | `GET /admin/login`、`POST /admin/login` | メールアドレス、パスワード | 主催者のイベント一覧 |
| 主催者のイベント一覧 | `GET /admin/events` | 自分が作ったイベント、販売開始日時、残数 | 販売状況、イベント作成 |
| イベント作成 | `GET /admin/events/new`、`POST /admin/events` | 日本語と英語の題名、開催日時、定員、価格、販売開始日時 | 販売状況 |
| 販売状況 | `GET /admin/events/{id}` | 定員、保留中、購入済み、残数、払い戻しの件数 | 主催者のイベント一覧 |
| ログアウト | `POST /admin/logout` | なし | 主催者ログイン |

購入の入力に誤りがあるときと売り切れのときは、詳細の画面にエラー文を出して返します。照会でメールアドレスとトークンの購入が一致しないときは、どちらが違うかを区別せずに同じ文を出します。

## 文字列の言語

利用者が読む文字列は日本語と英語で持ちます。対象は画面、メール、クライアントへ返すエラー文です。ログ、サーバー内部のエラー文、CLIの出力は英語だけで書きます。

| 項目 | 設計 |
|---|---|
| 置き場 | `internal/i18n`に、キーから文への`map[string]string`を言語ごとに置く |
| 漏れの検出 | 両言語のキーの集合が一致し、空の文がないことをテストで確かめる |
| 言語の判定 | `Accept-Language`を`golang.org/x/text/language`の`Matcher`で日本語と英語に照合する。日本語を先頭に置き、どちらにも合わないときは日本語にする |
| 応答の見出し | `Content-Language`を付け、`Vary: Accept-Language, HX-Request`を付ける |
| イベントの題名 | 主催者が両言語で入力し、判定した言語の題名を出す |
| エラー文の変換 | ドメインのエラー型を、HTTP層の1か所でキーへ変える。対応のないエラーは汎用の文にし、詳細は英語でログに書く |

## メール

メールは`Mailer`の差し替え口から送ります。模擬実装`internal/mail/fake`は送った内容をメモリに残し、テストから読めるようにします。実際の送信サービスの実装は作りません。

| 種類 | 契機 | 本文に入れるもの |
|---|---|---|
| 保留の通知 | `held`への遷移 | イベントの題名、枚数、決済の期限、期限までに決済を終えないと席は確保されないこと、照会トークン付きのURL。`checkout_url`があれば決済を続けるリンク |
| 購入確定 | `purchased`への遷移 | イベントの題名、開催日時、整理番号、照会トークン付きのURL |
| 払い戻し | `refunding`への遷移 | イベントの題名、払い戻す金額（`purchases.amount`）、照会トークン付きのURL |

メールと画面に出す決済の期限は`expires_at`を日本時間で示し、英語の文には`+09:00`を添えます。メールの言語は購入を作ったときに判定した言語を`purchases.lang`に保存して決めます。決済の完了通知と払い戻しはリクエストの外で起こり、`Accept-Language`がないためです。

送信用のゴルーチンは5秒ごとに`mail_outbox`の未送信の行を`id`の順に読み、送れたら`sent_at`を同期項目で書きます。保留の通知の行は送る前に購入を読みます。`held`でなければ送らず、`sent_at`だけを書く扱いです。決済の失敗や購入確定の後に「期限までに決済を」というメールが届かないようにするためです。`held`のまま`payment_id`がまだ空なら、決済を続けるリンクを入れられるように、その行を次の回へ回します。保留の作成（`expires_at`の10分前）から1分を過ぎても`payment_id`が空なら、リンクを入れずに送る決まりです。送信に失敗した行は飛ばして次の行へ進み、次の回にもう一度送ります。同じ行が10回続けて失敗したら`ErrorReporter`で1回だけ報告し、その後も送り直しを続ける仕組みです。送れたときや送らずに`sent_at`を書いたときは、その行の回数の記録を消します。回数を数える場所はメモリです。送信の後で`sent_at`を書く前に落ちると同じメールをもう1回送るので、メールは少なくとも1回届き、重複することがあります。

## 観測

観測の初期の構成はCloudWatchです。移る条件と手順はADR-0005にあります。

| 信号 | firstlineの出し方 | CloudWatchでの置き場 |
|---|---|---|
| ログ | `log/slog`のJSONを標準出力へ1行ずつ書く。各行にリクエストIDを入れる | systemdが`/var/log/firstline/app.log`へ追記し、CloudWatch Agentがファイルの行をそのままロググループ`/firstline/app`へ送る |
| 指標 | `MetricsSink`のEMF実装が、`/healthz`と同じ値を60秒ごとに1行のJSONにして`127.0.0.1:25888`へTCPで送る | 名前空間`firstline`の指標 |
| エラー | `ErrorReporter`の初期の実装が、ERRORのログを書く | メトリクスフィルタ`{ $.level = "ERROR" }`で件数を指標にする。フィルタの既定値は0にする |

差し替え口は次のとおりです。

```go
type ErrorReporter interface {
	Report(ctx context.Context, err error, attrs ...slog.Attr)
}

type MetricsSink interface {
	Emit(ctx context.Context, s HealthSnapshot) error
}
```

`HealthSnapshot`は`/healthz`が返す項目を持つ構造体です。EMFの各行には送り先のロググループ`/firstline/metrics`を示す`LogGroupName`を入れます。`Emit`がエラーを返しても購入の処理には影響させず、捨てた回数をログに書きます。

CloudWatchのアラームは次のとおりです。通知はSNSのトピックからメールで送ります。EMFの指標のアラームでは指標が届かないときも異常とみなします。値が届かない時間を正常として扱うのはエラーのアラームだけです。[メトリクスフィルタ](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/MonitoringLogData.html)は、ログの取り込みがない1分間には既定値も報告しないためです。

| アラーム | 条件 |
|---|---|
| エラー | ERRORのログが5分で1件以上 |
| 書き込みの詰まり | `queue_usage_avg_5m`が50%を超える |
| 規模拡大の判断 | `sync_items_per_sec_max_5m`が、ADR-0003の限界負荷の50%を超える |
| WALの伸び | `wal_bytes`が1GiBを超える |
| ディスク | CloudWatch Agentのディスクの使用率が50%を超える |
| 証明書 | `tls_cert_days_left`が14を下回る |

## 設定とCLI

firstlineは環境変数で設定します。

| 環境変数 | 値 | 既定 |
|---|---|---|
| `FIRSTLINE_STORE` | `sqlite`、`postgres`、`mysql` | `sqlite` |
| `FIRSTLINE_DB_PATH` | SQLiteのファイルのパス | `/var/lib/firstline/firstline.db` |
| `FIRSTLINE_DSN` | PostgreSQLかMySQLの接続文字列。`FIRSTLINE_STORE`が`sqlite`のときは使わない | なし |
| `FIRSTLINE_INVENTORY` | `memory`、`db` | `memory` |
| `FIRSTLINE_ADDR` | 待ち受けるアドレス | `:443` |
| `FIRSTLINE_TLS_CERT`、`FIRSTLINE_TLS_KEY` | 証明書と秘密鍵のファイルのパス | `/etc/firstline/tls/fullchain.pem`、`/etc/firstline/tls/privkey.pem`。ファイルがないと起動しない |
| `FIRSTLINE_TOKEN_KEY` | 購入照会トークンの鍵 | なし。ないと起動しない |
| `FIRSTLINE_BASE_URL` | メールに書く照会URLの先頭。`https://`で始まり、末尾に`/`を付けない | なし。ないと起動しない |
| `FIRSTLINE_ERROR_REPORTER` | エラーの送り先の実装の名前。カンマで区切って複数を指定でき、すべてへ送る | `log` |
| `FIRSTLINE_METRICS` | `emf`、`none` | `emf` |
| `FIRSTLINE_EMF_ADDR` | EMFを送るCloudWatch Agentのアドレス | `127.0.0.1:25888` |
| `FIRSTLINE_ADMIN_ADDR` | `/healthz`を返す運用者向けの待ち受け | `127.0.0.1:9090` |
| `FIRSTLINE_ADMIN_SOCK` | 主催者の追加を受けるUnixソケットのパス | `/run/firstline/admin.sock` |
| `FIRSTLINE_LOCK_PATH` | 書き込むプロセスを1つにするロックファイルのパス | `/var/lib/firstline/firstline.lock` |
| `FIRSTLINE_HOLD_RATE_PER_MIN` | IPアドレスごとの1分あたりの保留の作成の上限。0で制限を外す | `10` |

バイナリは次のサブコマンドを持ちます。

| サブコマンド | 働き |
|---|---|
| `serve` | サービスとして動く |
| `create-organizer --email <メールアドレス>` | 主催者を作る。パスワードは標準入力から読む。`serve`が動いていれば`FIRSTLINE_ADMIN_SOCK`へ追加を頼み、`serve`の書き込みゴルーチンが書く。`serve`が動いていなければロックを取って自分で書く |
| `migrate-data --from <接続文字列> --to <接続文字列>` | 空の移行先へ表ごとに行を移し、行数と金額の合計を移行元と移行先で比べる。一致しなければ終了コード1で終わる。移行先がPostgreSQLなら、比べた後に、行がある場合だけ`mail_outbox.id`の採番を`setval`で最大値に合わせる（ADR-0003） |
| `reconcile --since <時刻>` | 指定した時刻からの完了済みの決済を`Payment.Completed`で取り、完了通知と同じ処理に流す。払い戻しまで終えてから終わる。1件でも処理できなければ終了コード1で終わる |

`serve`、`reconcile`、`migrate-data`と、`serve`が動いていないときの`create-organizer`は、どれも`FIRSTLINE_LOCK_PATH`を`flock`で取ってから自分の書き込みゴルーチンを始めます。`create-organizer`は`serve`が動いている間はロックを取らずに`serve`へ追加を頼むので、主催者を足しても販売は止まりません。`FIRSTLINE_ADMIN_SOCK`は所有者を`firstline`、権限を`0600`にしたUnixソケットで、`create-organizer`は`firstline`のユーザーで実行します。同じホストのほかのユーザーは主催者を作れません。ロックを取れなければ終了コード1で終わるので、WriteDBへ書くプロセスは常に1つです。

`create-organizer`はまずソケットへ接続します。接続できれば`serve`が動いているとみなし、ソケットの上のHTTPで`POST /organizers`にメールアドレスとパスワードのJSONを送ります。`serve`がArgon2idでハッシュを計算し、主催者の作成を同期項目として書く流れです。状態コードは作れたら201、メールアドレスがすでにあれば409、入力の誤りなら400です。入力の誤りは2つあります。1つめは`net/mail.ParseAddress`で読めないか、読んだ`Address`が入力と一致しないメールアドレスです。表示名付きの形はこの規則で400になります。2つめは`utf8.RuneCountInString`で数えて12文字に満たないパスワードです。保存するメールアドレスは小文字にそろえ、一意の判定、ログインの照合、試行回数の数え方も小文字にした値で行います。`POST /organizers`はソケットの`http.Server`にだけ登録し、443番と`FIRSTLINE_ADMIN_ADDR`では返しません。`create-organizer`は201なら終了コード0、それ以外は1で終わります。ソケットがないか接続を拒まれたときは、ロックを取って自分で書く決まりです。自分で書くときも同じ入力の検証と重複の判定を行い、作れたら終了コード0、それ以外は1で終わります。接続がほかの理由（権限がないなど）で失敗したときの終了コードは1で、何も書きません。`serve`の起動の手順1から7の間はソケットがなくロックも取れないので、終了コード1で終わり、起動の後にもう一度実行します。

## 配備

ホストはAWSのEC2の1台で、OSはUbuntu Server 24.04 LTSです。systemdでfirstlineとLitestreamのサービスを置き、CloudWatch Agentも同じホストで動かします。TLSは前段のプロキシを置かずにfirstlineが自分で終える構成です。EC2のセキュリティグループは443番と、証明書の検証に使う80番を外へ開きます。インスタンスロールには`CloudWatchAgentServerPolicy`を付けます。

| 項目 | `firstline.service` | `litestream.service` |
|---|---|---|
| 実行ユーザー | `firstline` | `firstline` |
| 環境変数のファイル | `/etc/firstline/env` | `/etc/firstline/litestream.env` |
| 再起動 | `Restart=on-failure` | `Restart=always` |
| 停止 | `KillSignal=SIGTERM`、`TimeoutStopSec=30` | 既定 |
| 再読み込み | `ExecReload=/bin/kill -HUP $MAINPID` | 使わない |
| 標準出力 | `StandardOutput=append:/var/log/firstline/app.log` | 既定（journald） |
| 実行時のディレクトリ | `RuntimeDirectory=firstline`（`/run/firstline/`） | 使わない |
| ログのディレクトリ | `LogsDirectory=firstline`（`/var/log/firstline/`） | 使わない |
| 書ける場所 | `ReadWritePaths=/var/lib/firstline` | `ReadWritePaths=/var/lib/firstline` |
| ポート443 | `AmbientCapabilities=CAP_NET_BIND_SERVICE` | 使わない |

`/var/log/firstline/app.log`はlogrotateで切り替えます。設定は`daily`、`maxsize 100M`、`rotate 7`、`compress`、`copytruncate`です。[logrotate](https://man7.org/linux/man-pages/man8/logrotate.8.html)は普段は1日に1回だけ動き、`maxsize`も動いた時点でしか見ません。そこで`logrotate.timer`のdrop-inで`OnCalendar=hourly`にし、毎時に大きさを見る構成です。ファイル1つの大きさは100MBに1時間分のログを足した量までに収まります。systemdは追記のモードでファイルを開くので、`copytruncate`で切り詰めた後も先頭から書き直されます。失われうるのは切り詰めの直前に書かれた数行のログだけで、購入の記録はDBにあるので影響しません。

### 証明書

証明書はLet's Encryptから[certbot](https://eff-certbot.readthedocs.io/en/stable/using.html)のstandalone方式で取ります。standalone方式は検証のためにポート80で待ち受けます。firstlineは443番だけを使うので、ポート80を取り合うことはありません。

最初の証明書は`certbot certonly --standalone -d <ドメイン> --deploy-hook /usr/local/bin/firstline-cert-deploy`で取ります。`--deploy-hook`は発行と更新のどちらでも、成功したときだけ動きます。certbotはこの指定を更新の設定に保存するので、以後の更新でも同じスクリプトが動く仕組みです。更新そのものはcertbotのパッケージが入れるsystemdのタイマーが`certbot renew`を定期的に実行して行います。

`/usr/local/bin/firstline-cert-deploy`の手順は次のとおりです。

1. `$RENEWED_LINEAGE`の`fullchain.pem`と`privkey.pem`を、`install -m 0600 -o firstline`で`/etc/firstline/tls/`の一時ファイルへ置き、`mv`で正式の名前に置き換えます。秘密鍵がほかの利用者に読める時間はできません。
2. `systemctl try-reload-or-restart firstline`を実行します。firstlineが動いていればSIGHUPを送り、動いていなければ何もしません。

最初の発行ではfirstlineがまだ動いていないので、スクリプトの手順2は何もしません。その後で`firstline`のサービスを起動してください。`FIRSTLINE_TLS_CERT`と`FIRSTLINE_TLS_KEY`は`/etc/firstline/tls/fullchain.pem`と`/etc/firstline/tls/privkey.pem`を指します。

firstlineは`http.Server`の`TLSConfig.Certificates`を空のままにし、`ListenAndServeTLS("", "")`で待ち受けます。`Certificates`が空なら、どの接続でも`GetCertificate`が呼ばれるためです。SIGHUPを受けると証明書と秘密鍵を読み直し、`GetCertificate`が返す証明書を`atomic.Pointer`で入れ替えます。SIGHUPの受け口は起動の最初に作ります。受け口がない間にSIGHUPが届くと、Goのプロセスは終了し、systemdの`Restart=on-failure`はその終了を再起動しないためです。読み直しに失敗したら古い証明書を使い続け、ERRORのログを書きます。再起動しないので、更新の時刻が販売中でも接続は途切れません。

更新の失敗に気づくため、firstlineは使っている証明書の期限までの日数を`/healthz`の`tls_cert_days_left`で返します。アラームが鳴るのはこの値が14日を切ったときです。

LitestreamはWriteDBのファイルを1秒ごとにCloudflare R2へ送ります。使う版はv0.5.10以上です。`restore`の`-integrity-check`がこの版で入ったためです（[restoreの文書](https://litestream.io/reference/restore/)）。設定ファイル`/etc/litestream.yml`は次のとおりです。

```yaml
dbs:
  - path: /var/lib/firstline/firstline.db
    replica:
      type: s3
      bucket: firstline-backup
      path: firstline.db
      endpoint: ${CF_ACCOUNT_ID}.r2.cloudflarestorage.com
      region: auto
      access-key-id: ${R2_ACCESS_KEY}
      secret-access-key: ${R2_SECRET_KEY}
```

Litestreamの複製は非同期です。[Litestreamの文書](https://litestream.io/tips/)によれば、ホストが壊れたときに失う範囲は直前の約1秒の変更です。この損失はADR-0001で制約として受け入れました。

アプリは`wal_autocheckpoint=0`なので、Litestreamが止まるとWALが伸び続けます。

CloudWatch Agentの設定は次のとおりです。journaldの収集に対応した版のCloudWatch Agentを使います。

```json
{
  "agent": { "metrics_collection_interval": 60 },
  "metrics": {
    "metrics_collected": {
      "disk": { "resources": ["/"], "measurement": ["used_percent"] }
    }
  },
  "logs": {
    "logs_collected": {
      "files": {
        "collect_list": [
          { "file_path": "/var/log/firstline/app.log", "log_group_name": "/firstline/app", "log_stream_name": "{instance_id}", "retention_in_days": 30 }
        ]
      },
      "journald": {
        "collect_list": [
          { "log_group_name": "/firstline/litestream", "log_stream_name": "{instance_id}", "units": ["litestream"], "retention_in_days": 30 }
        ]
      }
    },
    "metrics_collected": { "emf": {} }
  }
}
```

### `/healthz`

`/healthz`は運用者向けのJSONを返します。返すのは`FIRSTLINE_ADMIN_ADDR`の運用者向けの待ち受けだけで、外へ開いた443番では返しません。ADR-0003の判断に使う値もここから取ります。

| 項目 | 内容 |
|---|---|
| `commit_wait_p99_ms` | 直近5分の、同期項目の`Do`を呼んでからコミットの結果を受け取るまでの時間のp99。チャネルの空きを待った時間も含む |
| `sync_items_per_sec_max_5m` | 直近5分の、1秒ごとに書き込みゴルーチンが受け取った同期項目の数の最大値 |
| `queue_len`、`queue_cap` | 書き込みチャネルの今の長さと容量 |
| `queue_usage_avg_5m` | 直近5分の、書き込みチャネルの使用率の平均 |
| `dropped_total` | 捨てた非同期項目の件数 |
| `wal_bytes` | WALファイルの大きさ。1GiBを超えたら警告をログに書く |
| `db_bytes` | DBファイルの大きさ |
| `last_reconcile_at` | 最後に決済の照合が成功した時刻 |
| `tls_cert_days_left` | 使っている証明書の期限までの日数 |

### 復元

復元に使うのは`scripts/restore.sh`です。スクリプトの手順は次のとおりです。

1. `firstline`と`litestream`のサービスを止めます。
2. 今の`firstline.db`、`firstline.db-wal`、`firstline.db-shm`を、時刻付きの退避ディレクトリへ移します。
3. `litestream restore -config /etc/litestream.yml -integrity-check quick /var/lib/firstline/firstline.db`を実行します。引数に時刻を渡したときは`-timestamp`を付けます。
4. 復元したファイルの所有者を`firstline`に変えます。
5. `litestream`のサービスを起動します。
6. `firstline`のサービスを止めたまま`firstline reconcile --since <復元した時点の10分前>`を実行し、復元した時点より後に完了した決済を処理します。記録の残っていない購入の決済は`orphan_refunds`に入り、記録が`held`や`expired`で残った購入は遷移表のとおりに進みます。
7. `firstline`のサービスを起動します。在庫は起動時に作り直すので、手作業は要りません。

復元の手順は月に1回、別のホストで試してかかった時間とDBファイルの大きさを記録します。この記録はADR-0003の判断の材料です。

## 品質ゲート

変更のたびに通す検証は次のとおりです。

| 検証 | 内容 |
|---|---|
| 整形と静的解析 | `go fmt`、`go vet`、golangci-lint、staticcheck |
| ビルド | `go build ./...` |
| Goの書き方 | Goファイルを編集する前にmodern-go-guidelinesの`list --file-path`で指針を確かめ、当てはまる指針に従う |
| テスト | `go test -race --count=1 ./...`。SQLiteは一時ファイルで、PostgreSQLとMySQLはtestcontainersで、3実装に同じテストを通す |
| カバレッジ | 80%以上 |
| 変異テスト | mainから分かれた後の変更行の変異をすべて殺す。テストが実行しない行の変異も失敗とし、定数宣言の変異だけを除く |
| CRAP | 関数ごとに15以下 |
| 秘密情報 | gitleaks |
| E2E | 購入、期限切れ後の払い戻し、照会、主催者のログインからイベント作成までを実際のバイナリで通す |
| 状態遷移 | `spec/Purchase.tla`をTLCで確かめる |
| 文書 | `tools/doclint`が0件 |
