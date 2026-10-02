/*
 * SPDX-FileCopyrightText: mk-go project
 * SPDX-License-Identifier: AGPL-3.0-only
 */
import { describe, expect, it } from 'vitest';
import { selectedBuild } from './character-selection.js';
import type { BuildCharacter, ShowcaseCharacter } from './api.js';

describe('showcase character selection', () => {
	const showcase = [1, 2, 3].map(avatarId => ({ avatarId, level: 90, element: '', icon: '' } satisfies ShowcaseCharacter));
	const characters = [3, 1].map(avatarId => ({ avatarId } as BuildCharacter));
	it('matches reordered details by avatar ID', () => {
		expect(selectedBuild(showcase, characters, 0)?.avatarId).toBe(1);
		expect(selectedBuild(showcase, characters, 2)?.avatarId).toBe(3);
	});
	it('does not show another build for the omitted middle character', () => {
		expect(selectedBuild(showcase, characters, 1)).toBeNull();
		expect(selectedBuild(showcase, characters, null)).toBeNull();
		expect(selectedBuild(showcase, characters, 3)).toBeNull();
	});
	it('selects the twelfth preview and matches reordered details', () => {
		const previews = Array.from({ length: 12 }, (_, index) => ({ avatarId: index + 1, level: 90, element: '', icon: '' } satisfies ShowcaseCharacter));
		const details = [...previews].reverse().map(preview => ({ avatarId: preview.avatarId } as BuildCharacter));
		expect(selectedBuild(previews, details, 11)?.avatarId).toBe(12);
		expect(selectedBuild(previews, details, 12)).toBeNull();
	});
});
