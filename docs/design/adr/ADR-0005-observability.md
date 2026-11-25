# ADR-0005 観測をCloudWatchで始め、移る基準を決める

状態は採用です。

## 状況

firstlineはAWSのEC2の1台で動きます。障害に気づいて原因を調べるには観測の仕組みが必要です。最初からSentryのような専用のサービスを使うと、利用者が少ない時期にも費用がかかります。一方で、利用が増えるとCloudWatchだけではエラーの集約や調査に手間がかかる時期が来ることもありえます。このADRは初期の構成、移る条件、移る手順を先に決めておくためのものです。

## 決定

### 初期の構成

| 信号 | 出し方 | 送り先 |
|---|---|---|
| ログ | `log/slog`のJSONを標準出力へ書き、systemdがファイルへ追記する | CloudWatch Agentがファイルの行をCloudWatch Logsの`/firstline/app`へ送る |
| 指標 | `/healthz`の値を60秒ごとにEmbedded Metric Format（EMF）の1行のJSONにする | CloudWatch Agentの`tcp:25888`へ送り、CloudWatchの名前空間`firstline`の指標になる |
| エラー | `ErrorReporter`の差し替え口から報告する | 初期の実装はERRORのログを書く。CloudWatch Logsのメトリクスフィルタで件数を指標にし、アラームでメールを送る |

`ErrorReporter`の差し替え口は次のとおりです。

```go
type ErrorReporter interface {
	Report(ctx context.Context, err error, attrs ...slog.Attr)
}
```

初期の実装`internal/observe/logreporter`はERRORのログに、エラーの文、リクエストID、経路を書きます。Sentryなどの実装は作りません。移る条件を満たした時点で足します。

### 移る条件

次のどれか1つを満たしたら、エラーの送り先をSentryなどの専用のサービスへ移します。このADRで移す対象はエラーだけです。

| 観点 | 測るもの | 移る条件 | 理由 |
|---|---|---|---|
| 調査の時間 | 障害の記録にある、検知から原因の特定までの時間 | 原因の特定に月に許す停止時間60分を超えた障害が出る | 原因を追えない時間がそのまま停止の時間になるため |
| 費用 | AWSの請求にあるCloudWatchの月額（ログの取り込みと保存、指標、アラーム） | 同じ量のエラーを扱う専用のサービスの料金表の月額を超える | 移っても費用が増えないことを確かめてから移るため |
| 要件 | 要件として求められる観測 | ブラウザ側のエラーの収集、リリースごとのエラーの比較、同じ原因のエラーの集約のどれかが要件になる | 初期の構成（ログとメトリクスフィルタ）では、これらを扱わないため |

ログと指標はCloudWatchに残します。指標は`MetricsSink`の差し替え口から出しているので、送り先を替える必要が出ても業務ロジックは変わりません。

### 移る手順

1. 移行先の実装を`ErrorReporter`の差し替え口に足し、testcontainersか模擬のサーバーで試験を通します。
2. 環境変数`FIRSTLINE_ERROR_REPORTER`に両方の実装を並べて指定し、同じエラーを両方へ送ります。
3. 移行先の通知が、CloudWatchのアラームと同じ障害を拾うことを確かめます。
4. `FIRSTLINE_ERROR_REPORTER`から`log`を外し、CloudWatchのエラーのアラームを消します。

## 理由

CloudWatch Agentは[ファイルの行を集める設定](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/CloudWatch-Agent-Configuration-File-Details.html)を持ちます。ファイルへの追記はsystemdの`StandardOutput=append:`が行うので、firstlineは標準出力へ書くだけで済みます。JSONの行がそのままCloudWatch Logsに届き、メトリクスフィルタで`level`の値を判定できる点も、ファイルを選んだ理由です。指標は[EMF](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/CloudWatch_Embedded_Metric_Format_Generation_CloudWatch_Agent.html)の1行のJSONを送るだけで作れるため、AWSのSDKを足す必要がありません。

エラーの通知を差し替え口にするのは決済を差し替え口にしたのと同じ理由です。外部のサービスの鍵がなくてもCIですべての経路を動かせます。移るときも業務ロジックは変わりません。

## 結果

CloudWatch AgentがEC2の上で動くことが前提になります。EC2のインスタンスロールに`CloudWatchAgentServerPolicy`を付けます。

EMFの送信が失敗しても購入は止めません。送れなかった指標は捨て、捨てた件数をログに書きます。EMFの指標のアラームは欠損データを異常として扱う設定にし、指標が途切れたことに気づけるようにします。
