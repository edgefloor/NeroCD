#!/usr/bin/env python3
"""Validate release candidates and tag only versions with no published artifacts."""
import argparse
import base64
import json
import os
from pathlib import Path
import re
import subprocess
import urllib.error
import urllib.request


class ReleaseError(Exception):
    pass


def validate_inputs(tag, revision):
    # OCI tags cannot contain SemVer build metadata (+). Numeric identifiers
    # follow SemVer's no-leading-zero rule, including prerelease identifiers.
    number = r'(?:0|[1-9][0-9]*)'
    identifier = rf'(?:{number}|[0-9A-Za-z-]*[A-Za-z-][0-9A-Za-z-]*)'
    if not re.fullmatch(rf'v{number}\.{number}\.{number}(?:-{identifier}(?:\.{identifier})*)?', tag) or len(tag) > 128:
        raise ReleaseError('version must be an OCI-compatible vMAJOR.MINOR.PATCH with optional prerelease')
    if not re.fullmatch(r'[0-9a-f]{40}', revision):
        raise ReleaseError('revision must be a full lowercase commit SHA')


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise ReleaseError('publication lookup redirected; refusing to forward credentials')


def request_json(url, headers):
    request = urllib.request.Request(url, headers=headers)
    try:
        response = urllib.request.build_opener(NoRedirect).open(request, timeout=30)
    except urllib.error.HTTPError as error:
        response = error
    except (urllib.error.URLError, TimeoutError) as error:
        raise ReleaseError('publication lookup unavailable') from error
    with response:
        status = response.code
        try:
            payload = json.load(response)
        except (ValueError, UnicodeError) as error:
            raise ReleaseError('publication lookup returned invalid JSON') from error
    if not isinstance(payload, (dict, list)):
        raise ReleaseError('publication lookup returned an invalid object')
    return status, payload


def ensure_unpublished(tag, actor, credential, request=request_json):
    if not actor or not credential:
        raise ReleaseError('publication lookup requires credentials')
    headers = {'Authorization': f'Bearer {credential}', 'Accept': 'application/vnd.github+json',
               'X-GitHub-Api-Version': '2022-11-28'}
    api = 'https://api.github.com/repos/edgefloor/NeroCD'
    status, _ = request(api, headers)
    if status != 200:
        raise ReleaseError('cannot verify repository access')
    # The by-tag endpoint only returns published releases. List all pages so
    # uploaded assets on drafts also lock the version (contents:write token).
    page = 1
    while True:
        status, releases = request(f'{api}/releases?per_page=100&page={page}', headers)
        if status != 200 or not isinstance(releases, list):
            raise ReleaseError('cannot determine GitHub release publication status')
        for release in releases:
            if not isinstance(release, dict) or not isinstance(release.get('tag_name'), str):
                raise ReleaseError('malformed release listing')
            if release['tag_name'] == tag and (release.get('draft') is not True or release.get('assets') != []):
                raise ReleaseError('published release or uploaded assets lock this version')
        if len(releases) < 100:
            break
        page += 1
    basic = base64.b64encode(f'{actor}:{credential}'.encode()).decode()
    status, token = request('https://ghcr.io/token?service=ghcr.io&scope=repository:edgefloor/nerocd:pull',
                            {'Authorization': f'Basic {basic}'})
    if status != 200 or not isinstance(token, dict) or not isinstance(token.get('token'), str) or not token['token']:
        raise ReleaseError('cannot authenticate registry publication lookup')
    status, manifest = request(f'https://ghcr.io/v2/edgefloor/nerocd/manifests/{tag}',
                              {'Authorization': f'Bearer {token["token"]}',
                               'Accept': 'application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json'})
    if status == 200:
        raise ReleaseError('published image locks this version')
    errors = manifest.get('errors') if isinstance(manifest, dict) else None
    if status != 404 or not isinstance(errors, list) or not errors or any(
        not isinstance(error, dict) or error.get('code') not in {'MANIFEST_UNKNOWN', 'NAME_UNKNOWN'} for error in errors
    ):
        raise ReleaseError('cannot determine registry publication status')


def git(repo, *args):
    return subprocess.check_output(['git', '-C', str(repo), *args], text=True).strip()


def validate_commit(tag, revision, repo):
    validate_inputs(tag, revision)
    if git(repo, 'cat-file', '-t', revision) != 'commit':
        raise ReleaseError('revision must identify a commit')
    git(repo, 'merge-base', '--is-ancestor', revision, 'origin/main')
    return git(repo, 'show', '-s', '--format=%ct', revision)


def remote_tag(tag, repo):
    lines = git(repo, 'ls-remote', '--refs', 'origin', f'refs/tags/{tag}')
    return lines.split()[0] if lines else ''


def update_tag(tag, revision, repo, observed=None):
    validate_inputs(tag, revision)
    if observed is None:
        observed = remote_tag(tag, repo)
    git(repo, '-c', 'user.name=NeroCD release', '-c', 'user.email=release@users.noreply.github.com',
        'tag', '-f', '-a', tag, revision, '-m', f'Release {tag}')
    git(repo, '-c', 'credential.helper=', '-c', 'credential.helper=!gh auth git-credential',
        'push', f'--force-with-lease=refs/tags/{tag}:{observed}', 'origin', f'refs/tags/{tag}')
    if git(repo, 'ls-remote', 'origin', f'refs/tags/{tag}^{{}}').split()[0] != revision:
        raise ReleaseError('remote tag does not match validated revision')


def publish_tag(tag, revision, repo, actor, credential):
    validate_commit(tag, revision, repo)
    if git(repo, 'rev-parse', 'HEAD') != revision:
        raise ReleaseError('checkout differs from validated revision')
    # Observe first: a concurrent replacement during remote lookups must fail
    # the explicit push lease, never silently authorize a different candidate.
    observed = remote_tag(tag, repo)
    ensure_unpublished(tag, actor, credential)
    update_tag(tag, revision, repo, observed)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('command', choices=['validate', 'tag'])
    parser.add_argument('--tag', required=True)
    parser.add_argument('--revision', required=True)
    args = parser.parse_args()
    repo = Path.cwd()
    if args.command == 'validate':
        epoch = validate_commit(args.tag, args.revision, repo)
        output = f'tag={args.tag}\nrevision={args.revision}\nsource_date_epoch={epoch}\nimage=ghcr.io/edgefloor/nerocd\n'
        with open(os.environ['GITHUB_OUTPUT'], 'a', encoding='utf-8') as file:
            file.write(output)
    else:
        publish_tag(args.tag, args.revision, repo, os.environ['GITHUB_ACTOR'], os.environ['GH_TOKEN'])


if __name__ == '__main__':
    try:
        main()
    except (ReleaseError, subprocess.CalledProcessError) as error:
        raise SystemExit(f'release-candidate: {error}') from error
