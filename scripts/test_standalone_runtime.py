"""仅解析真实 Compose；不连接 Docker daemon、数据库或运行中的容器。"""

import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[1]
BUILD_ID = 'project-background-sha256-' + 'a' * 64


# 用途：在隔离目录中解析真实编排及合成运行配置。
# 参数：filename 为编排文件名；profile 为可选兼容配置。
# 返回值：Compose 规范化模型；解析失败时抛出断言异常。
def compose_model(filename, profile=None):
    with tempfile.TemporaryDirectory(prefix='project-compose-runtime-') as temporary:
        root = Path(temporary)
        source = root / filename
        source.write_text((ROOT / filename).read_text())
        runtime = root / 'runtime'
        runtime.mkdir()
        (runtime / 'project.env').write_text(
            'PROJECT_BACKGROUND_BUILD_ID=' + BUILD_ID + '\n'
            'PROJECT_BACKGROUND_HEALTH_ADDRESS=127.0.0.1:9193\n')
        for consumer in ('project-api', 'project-sla-notifier'):
            (runtime / ('license-' + consumer + '.env')).write_text(
                'COMMERCIAL_LICENSE_SERVICE_ID=' + consumer + '\n')
        empty = root / 'empty.env'
        empty.write_text('')
        environment = {
            'PATH': os.environ.get('PATH', '/usr/bin:/bin'),
            'PROJECT_RUNTIME_ENV_FILE': str(runtime / 'project.env'),
            'PROJECT_IMAGE_REF': 'example.invalid/project@sha256:' + 'a' * 64,
            'PROJECT_MYSQL_PASSWORD': 'synthetic-project-password',
            'PROJECT_MYSQL_ROOT_PASSWORD': 'synthetic-root-password',
        }
        arguments = ['docker', 'compose', '--env-file', str(empty), '-f', str(source)]
        if profile:
            arguments += ['--profile', profile]
        result = subprocess.run(
            arguments + ['config', '--format', 'json'], cwd=root, env=environment,
            text=True, capture_output=True, timeout=30)
        if result.returncode:
            raise AssertionError(result.stderr)
        return json.loads(result.stdout)


class StandaloneRuntimeTests(unittest.TestCase):
    # 用途：捕获独立生产默认漏后台或依赖已废弃内嵌开关的回归。
    # 参数：self 为当前测试实例。
    # 返回值：无；默认服务或后台依赖不符合约定时断言失败。
    def test_production_starts_separate_notifier_by_default(self):
        services = compose_model('compose.production.yaml')['services']
        self.assertIn('sla-notifier', services)
        self.assertNotIn('worker', services)
        api, notifier = services['project-api'], services['sla-notifier']
        self.assertEqual(notifier['entrypoint'], ['/usr/local/bin/sla-notifier'])
        self.assertEqual(api['image'], notifier['image'])
        for service in (api, notifier):
            self.assertEqual(service['environment']['PROJECT_RUN_WORKER_WITH_API'], 'false')
            self.assertEqual(service['environment']['PROJECT_BACKGROUND_ENGINE'], 'temporal')
            self.assertEqual(service['environment']['PROJECT_BACKGROUND_BUILD_ID'], BUILD_ID)
            self.assertEqual(service['depends_on']['migrate']['condition'],
                             'service_completed_successfully')
            self.assertIn('platform', service['networks'])
        self.assertEqual(notifier['stop_grace_period'], '7m0s')

    # 用途：捕获消费者许可文件共用、缺失或状态卷混用的回归。
    # 参数：self 为当前测试实例。
    # 返回值：无；每个消费者未加载独立材料或状态卷时断言失败。
    def test_production_preserves_separate_license_consumers(self):
        model = compose_model('compose.production.yaml')
        for service_name, consumer in (('project-api', 'project-api'),
                                       ('sla-notifier', 'project-sla-notifier')):
            with self.subTest(consumer=consumer):
                self.assertIn(service_name, model['services'])
                service = model['services'][service_name]
                self.assertEqual(service['environment'].get('COMMERCIAL_LICENSE_SERVICE_ID'), consumer)
                state = [volume for volume in service.get('volumes', [])
                         if volume['target'] == '/var/lib/commercial-license']
                self.assertEqual(len(state), 1)
                self.assertEqual(state[0]['source'], 'license-state-' + consumer)
                self.assertIn(state[0]['source'], model['volumes'])
                keys = [volume for volume in service['volumes']
                        if volume['target'] == '/app/data/keys/jwt-ed25519-public.pem']
                self.assertEqual(len(keys), 1)
                self.assertTrue(keys[0]['read_only'])

    # 用途：捕获兼容入口丢失、默认混入或绕过当前后台配置的回归。
    # 参数：self 为当前测试实例。
    # 返回值：无；兼容服务不能继承同一后台职责时断言失败。
    def test_split_profile_keeps_compatible_background_entrypoint(self):
        services = compose_model('compose.production.yaml', 'split-runtime')['services']
        worker = services['worker']
        self.assertEqual(worker['entrypoint'], ['/usr/local/bin/project-worker'])
        self.assertEqual(worker['environment'].get('PROJECT_BACKGROUND_ENGINE'), 'temporal')
        self.assertEqual(worker['environment']['PROJECT_BACKGROUND_BUILD_ID'], BUILD_ID)
        self.assertEqual(worker['environment']['COMMERCIAL_LICENSE_SERVICE_ID'], 'project-sla-notifier')
        self.assertEqual(worker['healthcheck'], services['sla-notifier']['healthcheck'])
        self.assertEqual(worker['volumes'], services['sla-notifier']['volumes'])

    # 用途：阻止仅启用兼容 profile 时额外启动同身份的后台消费者。
    # 参数：self 为当前测试实例。
    # 返回值：无；默认或兼容 profile 产生两个默认副本时断言失败。
    def test_profile_does_not_start_duplicate_background_consumers(self):
        for profile in (None, 'split-runtime'):
            with self.subTest(profile=profile):
                services = compose_model('compose.production.yaml', profile)['services']
                replicas = sum(service.get('scale', 1)
                               for name, service in services.items()
                               if name in ('sla-notifier', 'worker'))
                self.assertEqual(replicas, 1)
                if profile:
                    self.assertEqual(services['worker'].get('scale', 1), 0)

    # 用途：验证本地与独立生产探针使用容器运行配置中的后台监听地址。
    # 参数：self 为当前测试实例。
    # 返回值：无；探针错用 API 地址或宿主默认地址时断言失败。
    def test_notifier_probe_uses_its_runtime_health_address(self):
        for filename in ('compose.yaml', 'compose.production.yaml'):
            with self.subTest(compose=filename):
                services = compose_model(filename)['services']
                self.assertIn('sla-notifier', services)
                notifier = services['sla-notifier']
                self.assertEqual(notifier['environment'].get('PROJECT_BACKGROUND_ENGINE'), 'temporal')
                self.assertEqual(notifier['environment']['PROJECT_BACKGROUND_BUILD_ID'], BUILD_ID)
                probe = notifier['healthcheck']['test']
                self.assertEqual(probe[0], 'CMD-SHELL')
                with tempfile.TemporaryDirectory(prefix='project-probe-') as temporary:
                    wget = Path(temporary) / 'wget'
                    wget.write_text('#!/bin/sh\nprintf "%s\\n" "$@"\n')
                    wget.chmod(0o700)
                    environment = dict(notifier['environment'], PATH=temporary)
                    result = subprocess.run(
                        ['/bin/sh', '-c', probe[1].replace('$$', '$')],
                        env=environment, text=True, capture_output=True, timeout=5)
                    self.assertEqual(result.returncode, 0, result.stderr)
                    self.assertEqual(result.stdout.splitlines(),
                                     ['-q', '-O', '/dev/null', 'http://127.0.0.1:9193/healthz'])


if __name__ == '__main__':
    unittest.main()
