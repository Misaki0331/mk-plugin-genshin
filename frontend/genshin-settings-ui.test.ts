/*
 * SPDX-FileCopyrightText: mk-go project
 * SPDX-License-Identifier: AGPL-3.0-only
 */
import { afterEach, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen } from '@testing-library/vue';
import SettingsSection from './SettingsSection.vue';

const mocks = vi.hoisted(() => ({ api: vi.fn() }));
vi.mock('./api.js', () => ({ api: mocks.api }));
vi.mock('@/plugin-api.js', async () => {
	const { defineComponent, h } = await import('vue');
	const wrapper = defineComponent({ setup(_props, { slots }) { return () => h('div', slots.default?.()); } });
	return {
		MkFolder: wrapper,
		MkInput: wrapper,
		MkButton: defineComponent({ setup(_props, { slots }) { return () => h('button', slots.default?.()); } }),
		MkSwitch: defineComponent({
			props: { modelValue: Boolean, disabled: Boolean, helpText: String },
			emits: ['update:modelValue'],
			setup(props, { slots, emit }) {
				return () => h('div', [
					h('button', { role: 'switch', 'aria-checked': props.modelValue, disabled: props.disabled, onClick: () => emit('update:modelValue', !props.modelValue) }, slots.label?.()),
					h('button', { 'aria-label': props.helpText }, '?'),
				]);
			},
		}),
	};
});
afterEach(cleanup);

it('公開・参加設定はスイッチで操作し、補足はヘルプへ移す', async () => {
	const preferences = { publishUid: false, publishSignature: true, rankingEnabled: true };
	mocks.api.mockImplementation(async (path: string, params: unknown) => {
		if (path === 'me/preferences') return preferences;
		if (path === 'me') return { uids: [], limit: 1, pending: null };
		if (path === 'me/preferences/update') return params;
		throw new Error(path);
	});
	render(SettingsSection);
	const save = await screen.findByRole('button', { name: '公開・参加設定を保存' });
	await vi.waitFor(() => expect(screen.getByRole('switch', { name: 'UIDを公開する' }).getAttribute('aria-checked')).toBe('false'));
	expect(screen.getAllByRole('switch')).toHaveLength(3);
	expect(screen.queryByRole('checkbox')).toBeNull();
	expect(screen.queryByText(/既定で参加/)).toBeNull();
	for (const name of ['UID', 'ステータスメッセージ']) {
		const help = screen.getByRole('button', { name: new RegExp(`^${name}の公開設定はすべての連携UID`) });
		expect(help.previousElementSibling?.getAttribute('role')).toBe('switch');
	}
	expect(screen.getByRole('button', { name: /^参加を無効にすると/ }).previousElementSibling?.getAttribute('role')).toBe('switch');
	await fireEvent.click(screen.getByRole('switch', { name: 'UIDを公開する' }));
	await fireEvent.click(save);
	expect(mocks.api).toHaveBeenCalledWith('me/preferences/update', { ...preferences, publishUid: true });
});
