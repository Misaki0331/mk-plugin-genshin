<!--
SPDX-FileCopyrightText: mk-go project
SPDX-License-Identifier: AGPL-3.0-only
-->
<template>
<span :class="$style.root">
	<button type="button" class="_button" :class="$style.button" :aria-label="text" @click.stop><i class="ti ti-help-circle" aria-hidden="true"></i></button>
	<span role="tooltip" :class="$style.tooltip">{{ text }}</span>
</span>
</template>

<script lang="ts" setup>
defineProps<{ text: string }>();
</script>

<style lang="scss" module>
.root { position: relative; display: inline-flex; vertical-align: middle; }
.button { padding: 4px; color: var(--MI_THEME-fgTransparentWeak); }
.tooltip {
	display: none;
	position: absolute;
	top: 100%;
	left: 0;
	z-index: 10;
	width: min(280px, 65vw);
	padding: 10px;
	border-radius: 8px;
	background: var(--MI_THEME-panel);
	color: var(--MI_THEME-fg);
	box-shadow: 0 2px 12px color-mix(in srgb, var(--MI_THEME-fg) 20%, transparent);
	font-size: 85%;
	font-weight: normal;
	white-space: normal;
}
.root:hover .tooltip, .root:focus-within .tooltip { display: block; }
</style>
