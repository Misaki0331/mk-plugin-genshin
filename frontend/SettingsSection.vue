<!--
SPDX-FileCopyrightText: mk-go project
SPDX-License-Identifier: AGPL-3.0-only
-->
<template>
<MkFolder>
	<template #label>原神</template>
	<template #suffix>{{ uids.length }} / {{ limit }} 件</template>
	<div class="_gaps_m">
		<MkSwitch v-model="preferences.publishUid" :disabled="busy" helpText="UIDの公開設定はすべての連携UIDに適用します。ゲーム内やEnkaの公開設定は変更しません。すでに他サーバーへ渡った情報は、相手側のキャッシュ更新まで残る場合があります。"><template #label>UIDを公開する</template></MkSwitch>
		<MkSwitch v-model="preferences.publishSignature" :disabled="busy" helpText="ステータスメッセージの公開設定はすべての連携UIDに適用します。ゲーム内やEnkaの公開設定は変更しません。すでに他サーバーへ渡った情報は、相手側のキャッシュ更新まで残る場合があります。"><template #label>ステータスメッセージを公開する</template></MkSwitch>
		<MkSwitch v-model="preferences.rankingEnabled" :disabled="busy" helpText="参加を無効にすると、連携済みのすべてのUIDを集計・表示から除外します。UIDやステータスメッセージを非公開にしても、参加中の戦績とゲーム内ニックネームはランキングに表示されます。"><template #label>サーバー内ランキングに参加する</template></MkSwitch>
		<MkButton :disabled="busy" @click="savePreferences">公開・参加設定を保存</MkButton>
		<a href="/plugin/genshin/rankings">サーバー内の原神ランキング</a>
		<div v-for="uid in uids" :key="uid" class="_buttons">
			<span>UID {{ uid }}</span>
			<MkButton :disabled="busy" @click="unlink(uid)">連携を解除</MkButton>
		</div>
		<MkInput v-model="draft" type="text" :disabled="busy" placeholder="800000000">
			<template #label>連携する原神UID <HelpHint text="本人確認が完了したUIDだけをプロフィールに表示します。リモートサーバーの連携情報は上限に含みません。"/></template>
		</MkInput>
		<MkButton primary :disabled="busy || uids.length >= limit" @click="begin">紐づけコードを発行</MkButton>
		<div v-if="pending" class="_gaps_s">
			<div>確認対象: {{ pending.uid }}</div>
			<MkInput :modelValue="pending.code" readonly>
				<template #label>紐づけコード</template>
			</MkInput>
			<div>原神のステータスメッセージに上のコードを追加して保存し、一度ゲームからログアウトしてから「認証する」を押してください。</div>
			<HelpHint text="反映には時間がかかる場合があります。コードは発行から10分間有効です。"/>
			<div role="timer">残り {{ remaining }} 秒</div>
			<div v-if="waitSeconds > 0">反映待ちです。{{ waitSeconds }} 秒後に再確認できます。</div>
			<MkButton primary :disabled="busy || remaining === 0 || waitSeconds > 0 || pending.attempts >= 10" @click="verify">認証する</MkButton>
			<div v-if="remaining === 0">コードの有効期限が切れました。再発行してください。</div>
			<div v-else-if="pending.attempts >= 10">確認回数の上限に達しました。コードを再発行してください。</div>
		</div>
		<div v-if="message" role="status">{{ message }}</div>
	</div>
</MkFolder>
</template>

<script lang="ts" setup>
import { ref, computed, onMounted, onUnmounted } from 'vue';
import { MkInput, MkButton, MkFolder, MkSwitch } from '@/plugin-api.js';
import HelpHint from './HelpHint.vue';
import { api } from './api.js';
import type { LinkChallenge, MeResponse, VerifyResponse, Preferences } from './api.js';
import { verificationWaitMs, verificationWaitSeconds } from './verification-wait.js';

const uids = ref<string[]>([]);
const preferences = ref<Preferences>({ publishUid: true, publishSignature: true, rankingEnabled: true });
const limit = ref(1);
const draft = ref('');
const pending = ref<LinkChallenge | null>(null);
const busy = ref(false);
const message = ref('');
const now = ref(Date.now());
const verifyNotBefore = ref(0);
const remaining = computed(() => Math.max(0, Math.ceil(((pending.value ? Date.parse(pending.value.expiresAt) : 0) - now.value) / 1000)));
const waitSeconds = computed(() => verificationWaitSeconds(verifyNotBefore.value, pending.value ? Date.parse(pending.value.nextCheckAt) : 0, now.value));
let timer: number | undefined;

async function reload(): Promise<void> {
	preferences.value = await api<Preferences>('me/preferences');
	const me = await api<MeResponse>('me');
	uids.value = me.uids;
	limit.value = me.limit;
	pending.value = me.pending;
}

async function savePreferences(): Promise<void> {
	await run(async () => {
		preferences.value = await api<Preferences>('me/preferences/update', { ...preferences.value });
		message.value = '公開・参加設定を保存しました。';
	});
}

async function run(action: () => Promise<void>): Promise<void> {
	busy.value = true;
	message.value = '';
	try { await action(); } catch (err) {
		message.value = (err as { message?: string } | null)?.message ?? '処理に失敗しました。しばらく待って再確認してください。';
		try { await reload(); } catch { /* Preserve the current UI on temporary network failure. */ }
	} finally { busy.value = false; }
}

async function begin(): Promise<void> {
	await run(async () => {
		pending.value = await api<LinkChallenge>('me/begin', { uid: draft.value.trim() });
		now.value = Date.now();
	});
}

async function verify(): Promise<void> {
	if (pending.value == null || busy.value || waitSeconds.value > 0) return;
	const code = pending.value.code;
	now.value = Date.now();
	verifyNotBefore.value = now.value + verificationWaitMs;
	await run(async () => {
		const res = await api<VerifyResponse>('me/verify', { code });
		await reload();
		message.value = res.verified ? '連携が完了しました。ゲーム内の紐づけコードは削除できます。' : 'まだコードを確認できません。保存後にゲームからログアウトしたことを確認し、反映を待ってください。';
	});
}

async function unlink(uid: string): Promise<void> {
	await run(async () => { await api('me/unlink', { uid }); await reload(); message.value = '連携を解除しました。'; });
}

onMounted(async () => {
	timer = window.setInterval(() => { now.value = Date.now(); }, 1000);
	await run(reload);
});
onUnmounted(() => { if (timer != null) window.clearInterval(timer); });
</script>
