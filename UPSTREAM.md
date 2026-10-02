# 取得元と上流向け移植

- 取得元: https://github.com/shiroha-a/mk-plugin-genshin
- 保存先: https://github.com/Misaki-Project/mk-plugin-genshin
- 本人確認・複数UIDの取り込み元: 上流PR #2 の `d5091d02c7f70dcf15c40855c905d75f98790125`
- ローカルバージョン: `0.2.0`

幽境の公開API項目名は、[EnkaラッパーのGenshinUser](https://github.com/yuko1101/enka-network-api/blob/762c45a4e6f4546fe159863e3507285c744d9c3f/src/models/GenshinUser.ts) にある `stygianId`・`stygianIndex`・`stygianSeconds` と照合した。追加のHoYoLAB認証やCookieは使用しない。

本人確認付き UID 連携・複数 UID・ロールポリシー上限に、公開設定・ランキング・最大12件のキャラクター表示を追加する。先行実装 `151e135548d405fd39faddd38eeacf1dfaf51048` から、上流PR #2へ移植した。mk本体・role-level・Misaki独自の権限や「見つける」画面の変更は含まない。標準UIには関連frontend PRのプラグイン公開API追加が必要。ライセンスは `LICENSE` に従う。検証用コピーや生成物は含めない。

通常の `tools/pluginbuild` と `Dockerfile.bundled` のプラグイン探索対象になる。フロントエンドも含める場合は、同じソースからビルドしたアセットを `ASSETS_SOURCE=local` で供給する。外部アセットイメージには今回の UI は含まれない。

Migration 8 は旧連携を未確認データとして退避する。導入前に DB をバックアップし、既存利用者へ再認証を案内する。旧バージョンへ戻す場合は、イメージだけでなく導入前の DB も復元する。バックアップ取得後の書き込みは失われるため、復元を伴う切り戻しではサービスを停止する。

Migration 9 はユーザー単位の公開・参加設定テーブル、UIDごとの表示用ID、幽境の保存項目を追加する。Migration 9適用前への切り戻しも、イメージの差し替えだけでは完了しない。本人確認済みUIDごとに集計し、ランキング参加の無効化・凍結・削除を反映する。保存済みの公開設定を、閲覧用のUID表示スイッチで変更することはない。
