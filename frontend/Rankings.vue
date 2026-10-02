<!--
SPDX-FileCopyrightText: mk-go project
SPDX-License-Identifier: AGPL-3.0-only
-->
<template>
<component :is="embedded ? 'div' : PageWithHeader">
	<div class="_spacer" :class="$style.root">
		<div class="_gaps_m">
			<div class="_panel _gaps_m" :class="$style.panel">
				<MkSelect v-model="metric" :items="metricItems" :disabled="busy">
					<template #label>ランキングの種類</template>
				</MkSelect>
				<MkSwitch v-model="showUid">
					<template #label>公開UIDを表示する</template>
				</MkSwitch>
				<div :class="$style.description">本人確認済みのUIDごとに集計します。設定のプロフィール内の「原神」からランキング参加を無効にできます。</div>
			</div>
			<section class="_panel" :class="$style.panel" :aria-label="labels[metric]">
				<h2 :class="$style.heading"><i class="ti ti-trophy"></i> {{ labels[metric] }}</h2>
				<p v-if="metric === 'stygian'" :class="$style.description">開催ID {{ result?.scheduleId || '未取得' }}</p>
				<p v-if="error" role="alert">{{ error }}</p>
				<div v-else-if="busy" role="status" aria-label="読み込み中"><MkLoading/></div>
				<ol v-else-if="result?.entries.length" :class="$style.records">
					<li v-for="entry in result.entries" :key="entry.accountId" :class="$style.record">
						<b :class="[$style.rank, { [$style.medal]: entry.rank >= 1 && entry.rank <= 3 }]" :aria-label="`${entry.rank}位`">{{ rankDisplay(entry.rank) }}</b>
						<MkAvatar v-if="users[entry.userId]" :user="users[entry.userId]" :link="true" :class="$style.avatar"/>
						<i v-else class="ti ti-user" :class="$style.avatar" aria-hidden="true"></i>
						<div :class="$style.user">
							<MkUserName v-if="users[entry.userId]" :user="users[entry.userId]" :nowrap="true"/>
							<a v-else :href="`/users/${entry.userId}`">{{ entry.nickname }}</a>
							<div v-if="users[entry.userId] || (showUid && entry.uid)" :class="$style.detail">{{ entry.nickname }}<span v-if="showUid && entry.uid"> · UID {{ entry.uid }}</span></div>
						</div>
						<div :class="$style.score">
							<b v-if="metric === 'stygian'"><img v-if="difficultyImage(entry.difficulty)" :class="$style.difficulty" :src="difficultyImage(entry.difficulty)" :title="difficultyLabel(entry.difficulty)" :alt="difficultyLabel(entry.difficulty)" tabindex="0"/><span v-else>{{ difficultyLabel(entry.difficulty) }}</span> {{ entry.seconds }} 秒</b>
							<b v-else>{{ entry.value.toLocaleString() }}</b>
							<div :class="$style.detail">取得 <MkTime :key="entry.fetchedAt" :time="entry.fetchedAt"/></div>
						</div>
					</li>
				</ol>
				<p v-else>集計対象の記録はありません。</p>
				<div class="_buttons" :class="$style.pagination">
					<MkButton :disabled="busy || offset === 0" @click="offset = Math.max(0, offset - 50)">前の50件</MkButton>
					<MkButton :disabled="busy || !result?.hasMore || offset >= 10000" @click="offset += 50">次の50件</MkButton>
				</div>
			</section>
		</div>
	</div>
</component>
</template>

<script lang="ts" setup>
import { ref, watch } from 'vue';
import { MkButton, MkSelect, MkSwitch, MkAvatar, MkUserName, MkTime, MkLoading, PageWithHeader, useMkSelect, getUsers, definePage } from '@/plugin-api.js';
import type { PluginUser } from '@/plugin-api.js';
import { api } from './api.js';
import type { RankingResponse } from './api.js';
import { rankingParams, resolvedSchedule, rankingLabels as labels } from './ranking-query.js';
import { difficultyLabel, difficultyImage } from './stygian-difficulty.js';

const props = withDefaults(defineProps<{ embedded?: boolean }>(), { embedded: false });
const { model: metric, def: metricItems } = useMkSelect({
	items: [
		{ label: labels.spiral, value: 'spiral' },
		{ label: labels.achievements, value: 'achievements' },
		{ label: labels.friendship, value: 'friendship' },
		{ label: labels.stygian, value: 'stygian' },
	],
	initialValue: 'stygian',
});
const showUid = ref(false);

function rankDisplay(rank: number): string | number {
	return ['🥇', '🥈', '🥉'][rank - 1] ?? rank;
}

const users = ref<Record<string, PluginUser>>({});
const offset = ref(0);
const scheduleId = ref(0);
const result = ref<RankingResponse | null>(null);
const busy = ref(false);
const error = ref('');
watch(metric, () => { offset.value = 0; scheduleId.value = 0; });
watch([metric, offset], async ([kind, start], _old, onCleanup) => {
	let active = true;
	onCleanup(() => { active = false; });
	busy.value = true;
	error.value = '';
	result.value = null;
	try {
		const response = await api<RankingResponse>('rankings', rankingParams(kind, start, scheduleId.value));
		if (!active) return;
		const userIds = [...new Set(response.entries.map(entry => entry.userId))];
		const profiles = await getUsers(userIds).catch(() => []);
		if (active) {
			users.value = Object.fromEntries(profiles.map(user => [user.id, user]));
			scheduleId.value = resolvedSchedule(scheduleId.value, kind, response.scheduleId);
			result.value = response;
		}
	} catch {
		if (active) error.value = 'ランキングを取得できませんでした。しばらく待って再確認してください。';
	} finally {
		if (active) busy.value = false;
	}
}, { immediate: true });

if (!props.embedded) {
	definePage(() => ({ title: '原神ランキング', icon: 'ti ti-trophy' }));
}
</script>

<style lang="scss" module>
.root { --MI_SPACER-w: 800px; }
.panel { padding: 16px; }
.heading { margin: 0 0 12px; font-size: 1em; }
.description, .detail { font-size: 85%; color: var(--MI_THEME-fgTransparentWeak); }
.records { margin: 0; padding: 0; list-style: none; }
.record {
	display: grid;
	grid-template-columns: 28px 36px minmax(0, 1fr) auto;
	align-items: center;
	gap: 8px;
	padding: 12px 0;
	border-bottom: 1px solid var(--MI_THEME-divider);
	&:last-child { border-bottom: none; }
}
.rank { text-align: center; }
.medal { font-size: 1.5em; }
.difficulty { width: 32px; height: 32px; object-fit: contain; vertical-align: middle; }
.avatar { width: 36px; height: 36px; }
.user { min-width: 0; overflow-wrap: anywhere; }
.detail { margin-top: 4px; }
.score { text-align: right; font-variant-numeric: tabular-nums; }
.pagination { margin-top: 16px; }
@media (max-width: 480px) {
	.record { grid-template-columns: 24px 32px minmax(0, 1fr); }
	.avatar { width: 32px; height: 32px; }
	.score { grid-column: 3; text-align: left; }
}
</style>
