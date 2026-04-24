#!/bin/sh
set -e

picoclaw_home="${PICOCLAW_HOME:-${HOME}/.picoclaw}"
workspace_dir="${PICOCLAW_WORKSPACE:-${picoclaw_home}/workspace}"
skills_target="${PICOCLAW_WORKSPACE_SKILLS:-${workspace_dir}/skills}"
skills_source="${PICOCLAW_SKILLS_SOURCE:-/opt/picoclaw/skills}"
sync_skills="${PICOCLAW_SYNC_SKILLS:-1}"

sync_picoclaw_skills() {
    if [ "${sync_skills}" = "0" ] || [ "${sync_skills}" = "false" ]; then
        echo "Picoclaw skill sync disabled by PICOCLAW_SYNC_SKILLS=${sync_skills}"
        return 0
    fi

    if [ ! -d "${skills_source}" ]; then
        echo "WARN: Picoclaw skills source missing: ${skills_source}"
        return 0
    fi

    mkdir -p "${skills_target}"

    # Copy each skill directory independently so mounted workspace state does not
    # hide the immutable image bundle. Existing files are updated from source.
    for skill_dir in "${skills_source}"/*; do
        [ -d "${skill_dir}" ] || continue
        skill_name="$(basename "${skill_dir}")"
        mkdir -p "${skills_target}/${skill_name}"
        cp -R "${skill_dir}/." "${skills_target}/${skill_name}/"
    done

    copied_count="$(find "${skills_target}" -mindepth 2 -maxdepth 2 -name SKILL.md 2>/dev/null | wc -l | tr -d ' ')"
    echo "Picoclaw skills synced: source=${skills_source} target=${skills_target} skill_count=${copied_count}"
}

validate_active_livekit_skills() {
    active="${PICOCLAW_LIVEKIT_SKILLS:-}"
    [ -n "${active}" ] || return 0

    missing=""
    old_ifs="${IFS}"
    IFS=","
    for skill in ${active}; do
        skill="$(echo "${skill}" | xargs)"
        [ -n "${skill}" ] || continue
        if [ ! -f "${skills_target}/${skill}/SKILL.md" ]; then
            missing="${missing}${missing:+,}${skill}"
        fi
    done
    IFS="${old_ifs}"

    if [ -n "${missing}" ]; then
        echo "WARN: Missing active LiveKit skills: ${missing}; expected under ${skills_target}"
    else
        echo "Active LiveKit skills available: ${active}"
    fi
}

# First-run: neither config nor workspace exists.
# If config.json is already mounted but workspace is missing, skip onboard to
# avoid the interactive "Overwrite? (y/n)" prompt hanging in a non-TTY container.
if [ ! -d "${workspace_dir}" ] && [ ! -f "${picoclaw_home}/config.json" ]; then
    picoclaw onboard
    sync_picoclaw_skills
    validate_active_livekit_skills
    echo ""
    echo "First-run setup complete."
    echo "Edit ${picoclaw_home}/config.json (add your API key, etc.) then restart the container."
    exit 0
fi

sync_picoclaw_skills
validate_active_livekit_skills

if [ "$#" -eq 0 ]; then
    exec picoclaw gateway
fi

if [ "$1" = "picoclaw" ]; then
    exec "$@"
fi

exec picoclaw "$@"
