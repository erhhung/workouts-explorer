# Deployment Preference

After implementing and verifying application fixes, deploy the affected services
for the user's review unless the user explicitly asks not to deploy. Do not wait
for a separate deployment request.

# Image Tag Consistency

Keep Harbor's current image tags consistent with deployed image references.
Do not create extra dated/review tag aliases for UI-only rollouts.
