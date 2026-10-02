/*
 * SPDX-FileCopyrightText: mk-go project
 * SPDX-License-Identifier: AGPL-3.0-only
 */
const labels = ['イージー', 'ノーマル', 'ハード', 'マスター', 'エクストラ', 'アルティメット'];

export function difficultyLabel(difficulty?: number): string {
	return labels[(difficulty ?? 0) - 1] ?? '難易度不明';
}

export function difficultyImage(difficulty?: number): string | undefined {
	if (difficulty == null || !Number.isInteger(difficulty) || difficulty < 1 || difficulty > 6) return undefined;
	return `/api/plugin/genshin/asset/UI_LeyLineChallenge_Medal_${difficulty}.png`;
}
