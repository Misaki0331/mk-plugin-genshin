/*
 * SPDX-FileCopyrightText: mk-go project
 * SPDX-License-Identifier: AGPL-3.0-only
 */

import { definePlugin } from '@/plugin-api.js';
import { initApi } from './api.js';
import ProfileCards from './ProfileCards.vue';
import SettingsSection from './SettingsSection.vue';
import Rankings from './Rankings.vue';

export default definePlugin({
	name: 'genshin',
	pages: [{ path: '/rankings', component: Rankings, navTitle: '原神ランキング', navIcon: 'ti ti-trophy' }],

	setup(host) {
		initApi(host.api);

		// Vue コンポーネント形式で登録する。ホストのアプリ内で描画されるので
		// MkInput などが本体と同じ見た目・挙動で動く。
		host.slot('profile:info', { component: ProfileCards });

		// 未ログインでは設定画面自体が出ないが、念のため。
		if (host.me != null) {
			host.slot('settings:profile', { component: SettingsSection });
		}
	},
});
