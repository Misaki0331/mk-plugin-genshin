<!--
SPDX-FileCopyrightText: mk-go project
SPDX-License-Identifier: AGPL-3.0-only
-->
<template>
<div>
	<ProfileCard v-for="(profile, i) in profiles" :key="profile.accountId ?? profile.uid ?? i" :ctx="ctx" :profile="profile"/>
</div>
</template>
<script lang="ts" setup>
import { ref, watch } from 'vue';
import type { SlotContext } from '@/plugin-api.js';
import { api } from './api.js';
import type { LinkedProfile } from './api.js';
import ProfileCard from './ProfileCard.vue';

const props = defineProps<{ ctx: SlotContext }>();
const profiles = ref<LinkedProfile[]>([]);
watch(() => props.ctx.user?.id, async (userId, _previous, onCleanup) => {
	let active = true;
	onCleanup(() => { active = false; });
	profiles.value = [];
	if (userId == null) return;
	try {
		const res = await api<{ profiles: LinkedProfile[] }>('profiles', { userId });
		if (active) profiles.value = res.profiles;
	} catch { /* A plugin failure must not break the user's profile. */ }
}, { immediate: true });
</script>
